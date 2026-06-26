// SPDX-FileCopyrightText: 2020 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	promlabels "github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// metricNameExtractor is used to walk through a PromQL expression and extract
// time series (i.e. metric) names along with their label matchers.
type metricNameExtractor struct {
	logger logr.Logger

	// expr is the PromQL expression that the metricNameExtractor is working on.
	expr string

	// absentLabel is the set of label names whose values should be extracted
	// from metric selectors and included in the generated absent() call.
	absentLabel AbsentLabel

	// found maps each metric name to the per-VectorSelector occurrences seen
	// for that metric in the expression. Each entry of the outer slice is one
	// VectorSelector; each element of the inner slice is one label matcher
	// preserved from that selector (filtered down to the labels requested by
	// AbsentLabel). The matcher type (=, !=, =~, !~) is preserved so the
	// generated absent() call uses the same operator the source expression used.
	found map[string][][]labelMatcher
}

// labelMatcher is the minimal subset of promlabels.Matcher we carry from the
// PromQL parser into the absent() emitter. We can't use promlabels.Matcher
// directly because it lazily compiles regex values and we want a pure data
// shape that's cheap to deduplicate and stable to render.
type labelMatcher struct {
	Name  string
	Value string
	Type  promlabels.MatchType
}

var reCache sync.Map

// Visit implements the parser.Visitor interface.
func (mex *metricNameExtractor) Visit(node parser.Node, path []parser.Node) (parser.Visitor, error) {
	vs, ok := node.(*parser.VectorSelector)
	if !ok {
		return mex, nil
	}

	name := vs.Name
	if name == "" {
		// Check if the VectorSelector uses label matching against the internal `__name__`
		// label. For example, the expression `http_requests_total` is equivalent to
		// `{__name__="http_requests_total"}`.
		for _, v := range vs.LabelMatchers {
			if v.Name != "__name__" {
				continue
			}

			switch v.Type {
			case promlabels.MatchEqual, promlabels.MatchNotEqual:
				name = v.Value
			case promlabels.MatchRegexp, promlabels.MatchNotRegexp:
				// Currently, we don't create absence alerts for regex name
				// label matching.
				// However, there are cases where some alert expressions use
				// regexp matching even where an equality would suffice.
				// E.g.:
				//   {__name__=~"http_requests_total"}
				rx, err := getRegExp(v.Value)
				if err != nil {
					// We do not return on error here so that any subsequent
					// VectorSelector(s) get a chance to be processed.
					mex.logger.Error(err, "could not compile regex: "+v.Value, "expr", mex.expr)
					continue
				}
				if rx.MatchString(v.Value) {
					name = v.Value
				}
			}
		}
	}
	if name == "" {
		mex.logger.Error(errors.New("error while parsing PromQL query"),
			"could not find metric name for VectorSelector: "+vs.String(),
			"expr", mex.expr)
		return mex, nil
	}

	switch {
	case strings.Contains(mex.expr, "absent("+name) ||
		strings.Contains(mex.expr, fmt.Sprintf("absent({__name__=\"%s\"", name)):
		// Skip this metric if the there is already an absent function for it in the
		// original expression.
		// E.g. absent(metric_name) || absent({__name__="metric_name"})
	case name == "up":
		// Skip "up" metric, it is automatically injected by Prometheus to describe
		// Prometheus scraping jobs.
	default:
		// Collect every matcher whose label name is requested by AbsentLabel,
		// preserving the matcher type (=, !=, =~, !~). Previously only
		// MatchEqual was kept, which silently dropped legitimate selectors
		// like `{job=~".*compactor.*"}` and produced only the unhelpful bare
		// absent(metric) rule. Each matcher type renders directly into PromQL
		// in buildAbsentExpr, so the generated labeled rule mirrors what the
		// source expression actually selects.
		var matchers []labelMatcher
		if len(mex.absentLabel) > 0 {
			for _, lm := range vs.LabelMatchers {
				if lm.Name == "__name__" {
					continue
				}
				if !mex.absentLabel.Matches(lm.Name) {
					continue
				}
				matchers = append(matchers, labelMatcher{
					Name:  lm.Name,
					Value: lm.Value,
					Type:  lm.Type,
				})
			}
		}
		mex.found[name] = append(mex.found[name], matchers)
	}
	return mex, nil
}

// getRegExp returns compiled regexp from the map if exists or compile new one and store
// for the next use.
func getRegExp(s string) (*regexp.Regexp, error) {
	if re, ok := reCache.Load(s); ok {
		return re.(*regexp.Regexp), nil
	}

	re, err := regexp.Compile(s)
	if err != nil {
		return nil, err
	}

	reCache.Store(s, re)

	return re, nil
}

// AbsenceRuleGroupName returns the name of the RuleGroup that holds absence alert rules
// for a specific RuleGroup in a specific PrometheusRule.
func AbsenceRuleGroupName(promRule, ruleGroup string) string {
	return fmt.Sprintf("%s/%s", promRule, ruleGroup)
}

// promRulefromAbsenceRuleGroupName takes the name of a RuleGroup that holds absence alert
// rules and returns the name of the corresponding PrometheusRule that holds the actual
// alert definitions. An empty string is returned if the name can't be determined.
func promRulefromAbsenceRuleGroupName(ruleGroup string) string {
	sL := strings.Split(ruleGroup, "/")
	if len(sL) != 2 {
		return ""
	}
	return sL[0]
}

type ruleGroupParseError struct {
	cause error
}

// Error implements the error interface.
func (e *ruleGroupParseError) Error() string {
	return e.cause.Error()
}

// ParseRuleGroups takes a slice of RuleGroup that has alert rules and returns two
// new slices of RuleGroup: one for the bare absence rules (absent(metric_name)),
// and one for the labeled absence rules (absent(metric_name{label="value",...})).
//
// The labels specified in the keepLabel map will be carried over to the corresponding
// absence alerts unless templating (i.e. $labels) was used for these labels.
//
// When absentLabel is non-empty the feature generates two kinds of absence rules for
// each metric:
//
//  1. The original bare absence rule (absent(metric_name)) — always present, identical to
//     the pre-feature behaviour. Returned in the first slice. This rule fires whenever
//     the metric is completely absent from the scrape target, regardless of label values.
//
//  2. One additional labeled absence rule per distinct label-value combination found in
//     the VectorSelector(s) of the expression. Returned in the second slice. These
//     narrower rules fire when the metric is absent for a specific label dimension
//     (e.g. a particular namespace/pod pair). Each labeled rule has a unique alert name
//     derived from the label values, so they coexist in the same rule group without
//     collision.
//
// When absentLabel is empty the second slice is always nil.
//
// Within each output stream, alert-name duplicates are eliminated: if two different
// source alert rules in the same group (or across groups in the same PrometheusRule)
// reference the same metric, only the first absence rule with that alert name is kept.
// This prevents identical absent(...) expressions from being emitted multiple times
// when several source alerts share an underlying metric.
//
// The rule group names for the absence alerts have the format: promRuleName/originalGroupName.
func ParseRuleGroups(logger logr.Logger, in []monitoringv1.RuleGroup, promRuleName string, keepLabel KeepLabel, absentLabel AbsentLabel) (bare, labeled []monitoringv1.RuleGroup, err error) {
	bare = make([]monitoringv1.RuleGroup, 0, len(in))
	if len(absentLabel) > 0 {
		labeled = make([]monitoringv1.RuleGroup, 0, len(in))
	}

	// Dedupe by alert name across all rule groups of this PrometheusRule. The
	// same metric can be referenced by multiple source alert rules (e.g. alerts
	// for "down 5m" and "down 15m" both on the same metric); without this
	// guard, parseRule emits one absent(metric) per invocation and they pile
	// up in the output. We dedupe globally rather than per-group so that
	// identical bare/labeled absence rules in different source rule groups
	// also collapse to a single emission. The first occurrence wins.
	seenBare := make(map[string]bool)
	seenLabeled := make(map[string]bool)

	for _, g := range in {
		var bareRules, labeledRules []monitoringv1.Rule
		for _, r := range g.Rules {
			b, l, err := parseRule(logger, r, keepLabel, absentLabel)
			if err != nil {
				return nil, nil, &ruleGroupParseError{cause: err}
			}
			bareRules = appendUniqueByAlert(bareRules, b, seenBare)
			labeledRules = appendUniqueByAlert(labeledRules, l, seenLabeled)
		}

		groupName := AbsenceRuleGroupName(promRuleName, g.Name)
		if len(bareRules) > 0 {
			sortByAlert(bareRules)
			bare = append(bare, monitoringv1.RuleGroup{Name: groupName, Rules: bareRules})
		}
		if len(labeledRules) > 0 {
			sortByAlert(labeledRules)
			labeled = append(labeled, monitoringv1.RuleGroup{Name: groupName, Rules: labeledRules})
		}
	}
	return bare, labeled, nil
}

// appendUniqueByAlert appends rules from src to dst, skipping any whose Alert
// name is already in seen. seen is updated in place. This is the deduplication
// hook used by ParseRuleGroups to guarantee that each output rule group (and,
// transitively, each AbsencePrometheusRule) contains every absent(...)
// expression at most once.
func appendUniqueByAlert(dst, src []monitoringv1.Rule, seen map[string]bool) []monitoringv1.Rule {
	for _, r := range src {
		if seen[r.Alert] {
			continue
		}
		seen[r.Alert] = true
		dst = append(dst, r)
	}
	return dst
}

// sortByAlert sorts rules by Alert name in place. Stable to keep the
// per-metric emission order from parseRule visible in tests.
func sortByAlert(rules []monitoringv1.Rule) {
	sort.SliceStable(rules, func(i, j int) bool {
		return rules[i].Alert < rules[j].Alert
	})
}

var nonAlphaNumericRx = regexp.MustCompile(`[^a-zA-Z0-9]`)

// parseRule generates the corresponding absence alert rules for a given Rule.
// Since an alert expression can reference multiple time series therefore a slice
// of []monitoringv1.Rule is returned as multiple absence alert rules would be
// generated — one for each time series.
//
// Bare rules (absent(metric_name)) and labeled rules
// (absent(metric_name{label="value",...})) are returned in two separate slices
// so callers can route them into different AbsencePrometheusRule CRs.
//
// When absentLabel is non-empty, each metric yields:
//   - One bare absence rule in the first slice — identical to pre-feature behaviour.
//   - One labeled absence rule per distinct label-value combination, in the second slice.
//     Duplicate combos are deduplicated.
//
// When absentLabel is empty the second slice is always nil.
func parseRule(logger logr.Logger, in monitoringv1.Rule, keepLabel KeepLabel, absentLabel AbsentLabel) (bare, labeled []monitoringv1.Rule, err error) {
	// Do not parse recording rules.
	if in.Record != "" {
		return nil, nil, nil
	}
	// Do not parse alert rule if it has the no_alert_on_absence label.
	if in.Labels != nil && parseBool(in.Labels[labelNoAlertOnAbsence]) {
		return nil, nil, nil
	}

	exprStr := in.Expr.String()
	mex := &metricNameExtractor{
		logger:      logger,
		expr:        exprStr,
		absentLabel: absentLabel,
		found:       map[string][][]labelMatcher{},
	}
	p := parser.NewParser(parser.Options{})
	exprNode, err := p.ParseExpr(exprStr)
	if err == nil {
		err = parser.Walk(mex, exprNode, nil)
	}
	if err != nil {
		// TODO: remove newline characters from expression.
		// The returned error has the expression at the end because
		// it could contain newline characters.
		return nil, nil, fmt.Errorf("could not parse rule expression: %s: %s", err.Error(), exprStr)
	}
	if len(mex.found) == 0 {
		return nil, nil, nil
	}

	// Default labels.
	absenceRuleLabels := map[string]string{
		"context":  "absent-metrics",
		"severity": "info",
	}

	// Retain labels from the original alert rule.
	if ruleLabels := in.Labels; ruleLabels != nil {
		for k := range keepLabel {
			v := ruleLabels[k]
			if v != "" && !strings.Contains(v, "$labels") {
				absenceRuleLabels[k] = v
			}
		}
	}

	for m, occurrences := range mex.found {
		// makeAlertName constructs an alert name from the metric name plus an
		// optional suffix (used to distinguish labeled rules from the bare rule
		// and from each other).
		makeAlertName := func(suffix string) string {
			supportGroup := absenceRuleLabels[LabelSupportGroup]
			if supportGroup == "" {
				supportGroup = absenceRuleLabels[LabelTier]
			}
			var words []string
			for _, v := range []string{"absent", supportGroup, absenceRuleLabels[LabelService], m} {
				s := nonAlphaNumericRx.Split(v, -1)
				words = append(words, s...)
			}
			if suffix != "" {
				words = append(words, nonAlphaNumericRx.Split(suffix, -1)...)
			}
			// Avoid name stuttering
			var alertName strings.Builder
			var prevW string
			for _, v := range words {
				w := strings.ToLower(v)
				if w != prevW {
					fmt.Fprint(&alertName, cases.Title(language.English).String(w))
					prevW = w
				}
			}
			return alertName.String()
		}

		ann := map[string]string{
			"summary": "missing " + m,
			"description": fmt.Sprintf(
				"The metric '%s' is missing. '%s' alert using it may not fire as intended. "+
					"See <https://github.com/sapcc/absent-metrics-operator/blob/master/docs/playbook.md|the operator playbook>.",
				m, in.Alert,
			),
		}

		duration := monitoringv1.Duration("10m")

		// emit appends a single absence rule for metric m using the given label
		// combo to the target slice. An empty combo produces the bare absent(m)
		// rule; a non-empty combo produces a labeled rule with a unique
		// alert-name suffix. buildAbsentExpr and comboSuffix both already
		// treat an empty combo as "no matchers / no suffix", so the bare and
		// labeled cases share the same construction path.
		emit := func(dst *[]monitoringv1.Rule, combo []labelMatcher) {
			*dst = append(*dst, monitoringv1.Rule{
				Alert:       makeAlertName(comboSuffix(combo)),
				Expr:        intstr.FromString(buildAbsentExpr(m, combo)),
				For:         &duration,
				Labels:      absenceRuleLabels,
				Annotations: ann,
			})
		}

		// Always generate the original bare absence rule (combo == nil) into
		// the bare slice.
		emit(&bare, nil)

		// When absentLabel is set, also generate one additional labeled rule
		// per distinct label-value combination found in the VectorSelectors,
		// into the labeled slice. Empty combos are skipped because they would
		// duplicate the bare rule.
		if len(absentLabel) > 0 {
			for _, combo := range distinctLabelCombos(occurrences) {
				if len(combo) == 0 {
					continue
				}
				emit(&labeled, combo)
			}
		}
	}

	// Sort each output slice for consistent test results. ParseRuleGroups
	// re-sorts after deduping across rule groups, so this only matters for
	// callers that invoke parseRule directly (and for stable behaviour when
	// dedup is a no-op).
	sortByAlert(bare)
	sortByAlert(labeled)

	return bare, labeled, nil
}

// distinctLabelCombos returns the deduplicated set of matcher combos from
// occurrences. Each occurrence has already been filtered to the labels
// requested by AbsentLabel at collection time (see metricNameExtractor.Visit),
// so this function only needs to deduplicate identical combos.
//
// Two combos are considered identical iff they contain the same set of
// (name, type, value) triples, so `job=~".*compactor.*"` and `job="compactor"`
// remain distinct and each contributes its own labeled absence rule.
func distinctLabelCombos(occurrences [][]labelMatcher) [][]labelMatcher {
	seen := make(map[string]struct{})
	var result [][]labelMatcher
	for _, occ := range occurrences {
		key := matchersKey(occ)
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			result = append(result, occ)
		}
	}
	return result
}

// sortMatchers returns a copy of in sorted by label name (ascending). The
// emitted PromQL and the dedup key both rely on a deterministic key order so
// that {a="x",b="y"} and {b="y",a="x"} produce identical output.
func sortMatchers(in []labelMatcher) []labelMatcher {
	out := make([]labelMatcher, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// matchersKey returns a canonical string representation of a matcher slice
// for dedup purposes. Includes the match type so `=` and `=~` against the
// same name/value still produce distinct keys.
func matchersKey(m []labelMatcher) string {
	sorted := sortMatchers(m)
	var sb strings.Builder
	for _, lm := range sorted {
		sb.WriteString(lm.Name)
		sb.WriteString(lm.Type.String())
		sb.WriteString(lm.Value)
		sb.WriteByte(',')
	}
	return sb.String()
}

// comboSuffix returns a stable string derived from matcher values, suitable
// for appending to an alert name to make it unique.
// Names are sorted; values are joined with underscores. The non-alphanumeric
// splitter in makeAlertName then strips regex punctuation, so a value like
// ".*compactor.*" cleanly becomes "Compactor" in the final alert name.
// An empty or nil combo returns "", which makeAlertName treats as "no suffix".
func comboSuffix(combo []labelMatcher) string {
	sorted := sortMatchers(combo)
	parts := make([]string, 0, len(sorted))
	for _, lm := range sorted {
		parts = append(parts, lm.Value)
	}
	return strings.Join(parts, "_")
}

// buildAbsentExpr constructs the PromQL absent() expression for a metric with
// the given label matchers. Names are sorted for deterministic output, and
// each matcher is rendered with its source operator (=, !=, =~, !~) and a
// properly quoted value so regex characters and embedded quotes survive
// intact. An empty or nil matchers slice yields bare absent(metricName), so
// callers can use the same emission path for the bare rule and labeled rules.
// E.g.: buildAbsentExpr("my_metric", []labelMatcher{{Name: "job", Value: ".*compactor.*", Type: promlabels.MatchRegexp}})
// returns: absent(my_metric{job=~".*compactor.*"})
func buildAbsentExpr(metricName string, matchers []labelMatcher) string {
	if len(matchers) == 0 {
		return fmt.Sprintf("absent(%s)", metricName)
	}
	sorted := sortMatchers(matchers)
	parts := make([]string, 0, len(sorted))
	for _, lm := range sorted {
		parts = append(parts, fmt.Sprintf("%s%s%s", lm.Name, lm.Type.String(), strconv.Quote(lm.Value)))
	}
	return fmt.Sprintf("absent(%s{%s})", metricName, strings.Join(parts, ","))
}
