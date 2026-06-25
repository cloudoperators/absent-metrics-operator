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
// When absentLabel is non-empty, equality label matchers for those label names are
// extracted from each metric's VectorSelector(s) and included in the generated absent()
// call — provided all occurrences of that metric in the expression agree on the same
// value for that label. This makes absence alerts more precise and avoids false positives
// across label dimensions.
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

	out := make([]monitoringv1.Rule, 0, len(mex.found))
	for m, occurrences := range mex.found {
		// Generate an alert name from metric name. Example:
		//   network:tis_a_metric:rate5m -> Absent(Support Group|Tier)ServiceNetworkTisAMetricRate5m
		supportGroup := absenceRuleLabels[LabelSupportGroup]
		if supportGroup == "" {
			supportGroup = absenceRuleLabels[LabelTier] // use tier in case there is no support group
		}
		var words []string
		for _, v := range []string{"absent", supportGroup, absenceRuleLabels[LabelService], m} {
			s := nonAlphaNumericRx.Split(v, -1) // remove non-alphanumeric characters
			words = append(words, s...)
		}
		// Avoid name stuttering
		//
		// TODO: fix edge case when support_group or service label value has non-numeric
		// character and splitting it will still result in name stuttering because
		// matching with previous word (as we do below) does not work as the original word
		// has been split into multiple words.
		// Example: support_group = "containers", service = "go-pmtud",
		// and metric = "go_pmtud_sent_error_peer_total" will result in
		// "AbsentContainersGoPmtudGoPmtudSentErrorPeerTotal" as the alert name.
		var alertName strings.Builder
		var prevW string
		for _, v := range words {
			w := strings.ToLower(v) // convert to lowercase for comparison
			if w != prevW {
				fmt.Fprint(&alertName, cases.Title(language.English).String(w))
				prevW = w
			}
		}

		// TODO: remove the link from description and add a 'playbook' label,
		// when our upstream solution gets the ability to process hardcoded
		// links in the 'playbook' label.
		ann := map[string]string{
			"summary": "missing " + m,
			"description": fmt.Sprintf(
				"The metric '%s' is missing. '%s' alert using it may not fire as intended. "+
					"See <https://github.com/sapcc/absent-metrics-operator/blob/master/docs/playbook.md|the operator playbook>.",
				m, in.Alert,
			),
		}

		duration := monitoringv1.Duration("10m")
		out = append(out, monitoringv1.Rule{
			Alert:       alertName.String(),
			Expr:        intstr.FromString(buildAbsentExpr(m, occurrences, absentLabel)),
			For:         &duration,
			Labels:      absenceRuleLabels,
			Annotations: ann,
		})
	}

	// Sort alert rules for consistent test results.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Alert < out[j].Alert
	})

	return out, nil
}

// buildAbsentExpr constructs the PromQL absent() expression for a metric.
//
// When absentLabel is non-empty, equality label matchers are extracted from the
// per-occurrence maps and included in the absent() selector — but only for labels
// where every VectorSelector occurrence of the metric in the expression carries the
// same equality matcher value. Labels with inconsistent or absent values across
// occurrences are silently omitted.
//
// If no matchers qualify, or absentLabel is empty/nil, a bare absent(metricName)
// expression is returned (the default, pre-feature behaviour).
func buildAbsentExpr(metricName string, occurrences []map[string]string, absentLabel AbsentLabel) string {
	if len(absentLabel) == 0 {
		return fmt.Sprintf("absent(%s)", metricName)
	}

	// Iterate over requested label names in a deterministic order.
	requestedLabels := make([]string, 0, len(absentLabel))
	for k := range absentLabel {
		requestedLabels = append(requestedLabels, k)
	}
	sort.Strings(requestedLabels)

	var matchers []string
	for _, labelName := range requestedLabels {
		var consistentValue *string
		consistent := true
		for _, occ := range occurrences {
			val, present := occ[labelName]
			if !present {
				// At least one occurrence has no equality matcher for this label;
				// omit it from the absent() selector.
				consistent = false
				break
			}
			if consistentValue == nil {
				v := val
				consistentValue = &v
			} else if *consistentValue != val {
				// Occurrences disagree on the value; omit this label.
				consistent = false
				break
			}
		}
		if consistent && consistentValue != nil {
			matchers = append(matchers, fmt.Sprintf(`%s="%s"`, labelName, *consistentValue))
		}
	}

	if len(matchers) == 0 {
		return fmt.Sprintf("absent(%s)", metricName)
	}
	return fmt.Sprintf("absent(%s{%s})", metricName, strings.Join(matchers, ","))
}
