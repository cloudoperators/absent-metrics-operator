// SPDX-FileCopyrightText: 2020 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
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

	// found maps each metric name to the list of label-value maps seen across
	// all VectorSelector occurrences of that metric in the expression. Each
	// element of the slice corresponds to one VectorSelector node.
	found map[string][]map[string]string
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
		// Collect equality label matchers for any requested absent labels.
		// Only MatchEqual matchers are considered because only they produce a
		// single deterministic value suitable for use in an absent() selector.
		matchers := make(map[string]string)
		if len(mex.absentLabel) > 0 {
			for _, lm := range vs.LabelMatchers {
				if lm.Name == "__name__" {
					continue
				}
				if lm.Type != promlabels.MatchEqual {
					continue
				}
				if mex.absentLabel[lm.Name] {
					matchers[lm.Name] = lm.Value
				}
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

// ParseRuleGroups takes a slice of RuleGroup that has alert rules and returns
// a new slice of RuleGroup that has the corresponding absence alert rules.
//
// The labels specified in the keepLabel map will be carried over to the corresponding
// absence alerts unless templating (i.e. $labels) was used for these labels.
//
// When absentLabel is non-empty the feature generates two kinds of absence rules for
// each metric:
//
//  1. The original bare absence rule (absent(metric_name)) — always present, identical to
//     the pre-feature behaviour. This rule fires whenever the metric is completely absent
//     from the scrape target, regardless of label values.
//
//  2. One additional labeled absence rule per distinct label-value combination found in
//     the VectorSelector(s) of the expression. These narrower rules fire when the metric
//     is absent for a specific label dimension (e.g. a particular namespace/pod pair).
//     Each labeled rule has a unique alert name derived from the label values, so they
//     coexist in the same rule group without collision.
//
// The rule group names for the absence alerts have the format: promRuleName/originalGroupName.
func ParseRuleGroups(logger logr.Logger, in []monitoringv1.RuleGroup, promRuleName string, keepLabel KeepLabel, absentLabel AbsentLabel) ([]monitoringv1.RuleGroup, error) {
	out := make([]monitoringv1.RuleGroup, 0, len(in))
	for _, g := range in {
		var absenceAlertRules []monitoringv1.Rule
		for _, r := range g.Rules {
			rules, err := parseRule(logger, r, keepLabel, absentLabel)
			if err != nil {
				return nil, &ruleGroupParseError{cause: err}
			}
			if len(rules) > 0 {
				absenceAlertRules = append(absenceAlertRules, rules...)
			}
		}

		if len(absenceAlertRules) > 0 {
			// Sort alert rules for consistent test results.

			sort.SliceStable(absenceAlertRules, func(i, j int) bool {
				return absenceAlertRules[i].Alert < absenceAlertRules[j].Alert
			})

			out = append(out, monitoringv1.RuleGroup{
				Name:  AbsenceRuleGroupName(promRuleName, g.Name),
				Rules: absenceAlertRules,
			})
		}
	}
	return out, nil
}

var nonAlphaNumericRx = regexp.MustCompile(`[^a-zA-Z0-9]`)

// parseRule generates the corresponding absence alert rules for a given Rule.
// Since an alert expression can reference multiple time series therefore a slice of
// []monitoringv1.Rule is returned as multiple absence alert rules would be generated —
// one for each time series.
//
// When absentLabel is non-empty, each metric yields:
//   - One bare absence rule (absent(metric_name)) — identical to pre-feature behaviour.
//   - One additional labeled absence rule per distinct label-value combination found in
//     the VectorSelector(s) of the expression. Duplicate combos are deduplicated.
func parseRule(logger logr.Logger, in monitoringv1.Rule, keepLabel KeepLabel, absentLabel AbsentLabel) ([]monitoringv1.Rule, error) {
	// Do not parse recording rules.
	if in.Record != "" {
		return nil, nil
	}
	// Do not parse alert rule if it has the no_alert_on_absence label.
	if in.Labels != nil && parseBool(in.Labels[labelNoAlertOnAbsence]) {
		return nil, nil
	}

	exprStr := in.Expr.String()
	mex := &metricNameExtractor{
		logger:      logger,
		expr:        exprStr,
		absentLabel: absentLabel,
		found:       map[string][]map[string]string{},
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
		return nil, fmt.Errorf("could not parse rule expression: %s: %s", err.Error(), exprStr)
	}
	if len(mex.found) == 0 {
		return nil, nil
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

	var out []monitoringv1.Rule
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

		// Always generate the original bare absence rule.
		out = append(out, monitoringv1.Rule{
			Alert:       makeAlertName(""),
			Expr:        intstr.FromString(fmt.Sprintf("absent(%s)", m)),
			For:         &duration,
			Labels:      absenceRuleLabels,
			Annotations: ann,
		})

		// When absentLabel is set, also generate one additional labeled rule per
		// distinct label-value combination found in the VectorSelectors.
		if len(absentLabel) > 0 {
			for _, combo := range distinctLabelCombos(occurrences, absentLabel) {
				if len(combo) == 0 {
					// No requested labels present in this occurrence — nothing to add
					// beyond the bare rule already emitted above.
					continue
				}
				labeledExpr := buildAbsentExpr(m, combo)
				// Build a suffix from sorted label values so the alert name is unique
				// and stable across runs.
				suffix := comboSuffix(combo)
				out = append(out, monitoringv1.Rule{
					Alert:       makeAlertName(suffix),
					Expr:        intstr.FromString(labeledExpr),
					For:         &duration,
					Labels:      absenceRuleLabels,
					Annotations: ann,
				})
			}
		}
	}

	// Sort alert rules for consistent test results.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Alert < out[j].Alert
	})

	return out, nil
}

// distinctLabelCombos returns the deduplicated set of label-value maps from
// occurrences, filtered to only the keys present in absentLabel.
// Each returned map contains only equality-matched labels that were requested.
// Maps that are identical after filtering are included only once.
func distinctLabelCombos(occurrences []map[string]string, absentLabel AbsentLabel) []map[string]string {
	seen := make(map[string]struct{})
	var result []map[string]string

	for _, occ := range occurrences {
		// Filter to requested keys only.
		filtered := make(map[string]string)
		for k, v := range occ {
			if absentLabel[k] {
				filtered[k] = v
			}
		}
		// Deduplicate by canonical string key.
		key := mapKey(filtered)
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			result = append(result, filtered)
		}
	}
	return result
}

// mapKey returns a canonical string representation of a label map for dedup purposes.
func mapKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(m[k])
		sb.WriteByte(',')
	}
	return sb.String()
}

// comboSuffix returns a stable string derived from label values, suitable for
// appending to an alert name to make it unique.
// Keys are sorted; values are joined with underscores.
func comboSuffix(combo map[string]string) string {
	keys := make([]string, 0, len(combo))
	for k := range combo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, combo[k])
	}
	return strings.Join(parts, "_")
}

// buildAbsentExpr constructs the PromQL absent() expression for a metric with
// the given label matchers map. Keys are sorted for deterministic output.
// E.g.: buildAbsentExpr("my_metric", map[string]string{"namespace":"prod","pod":"api"})
// returns: absent(my_metric{namespace="prod",pod="api"})
func buildAbsentExpr(metricName string, matchers map[string]string) string {
	if len(matchers) == 0 {
		return fmt.Sprintf("absent(%s)", metricName)
	}
	keys := make([]string, 0, len(matchers))
	for k := range matchers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, matchers[k]))
	}
	return fmt.Sprintf("absent(%s{%s})", metricName, strings.Join(parts, ","))
}
