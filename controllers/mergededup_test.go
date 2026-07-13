// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and absent-metrics-operator contributors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"errors"
	"testing"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// mkRuleGroup is a compact helper for building a monitoringv1.RuleGroup from a
// name and a list of (alert, expr) pairs — keeps the test tables readable.
func mkRuleGroup(name string, alertExprs ...string) monitoringv1.RuleGroup {
	if len(alertExprs)%2 != 0 {
		panic("mkRuleGroup: alertExprs must be pairs of (alert, expr)")
	}
	g := monitoringv1.RuleGroup{Name: name}
	for i := 0; i+1 < len(alertExprs); i += 2 {
		alert, expr := alertExprs[i], alertExprs[i+1]
		g.Rules = append(g.Rules, monitoringv1.Rule{
			Alert: alert,
			Expr:  intstr.FromString(expr),
		})
	}
	return g
}

// flattenGroups pulls the group name plus every (alert, expr) tuple out of a
// []RuleGroup so equality assertions don't have to know about intstr internals.
func flattenGroups(groups []monitoringv1.RuleGroup) [][3]string {
	var out [][3]string
	for _, g := range groups {
		for _, r := range g.Rules {
			out = append(out, [3]string{g.Name, r.Alert, r.Expr.String()})
		}
	}
	return out
}

func TestMergeAbsenceRuleGroups_DedupsAcrossSourcePRs(t *testing.T) {
	// Two source PRs (thanos-a, thanos-b) both emitted an absent()
	// rule for kube_node_status_condition. When thanos-b reconciles, the
	// existing CR already contains thanos-a's version; the merged result
	// must contain the rule exactly once.
	existing := []monitoringv1.RuleGroup{
		mkRuleGroup("thanos-a/node.alerts",
			"AbsentContainersKubeNodeStatusCondition", "absent(kube_node_status_condition)"),
	}
	fresh := []monitoringv1.RuleGroup{
		mkRuleGroup("thanos-b/node.alerts",
			"AbsentContainersKubeNodeStatusCondition", "absent(kube_node_status_condition)"),
	}

	got := flattenGroups(mergeAbsenceRuleGroups("thanos-b", existing, fresh))
	want := [3]string{"thanos-a/node.alerts", "AbsentContainersKubeNodeStatusCondition", "absent(kube_node_status_condition)"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("expected single deduped rule, got %#v", got)
	}
}

func TestMergeAbsenceRuleGroups_OrderIndependentWinner(t *testing.T) {
	// The winner for a colliding (Alert, Expr) must be the group whose name
	// sorts lex-smallest, regardless of whether the collision arrives via
	// existing or fresh. Otherwise successive reconciles ping-pong the same
	// pair of duplicates in and out of the aggregated CR.
	dup := "AbsentContainersKubeNodeStatusCondition"
	expr := "absent(kube_node_status_condition)"

	// Case 1: thanos-b reconciling; thanos-a already in existing.
	// Winner should be thanos-a (lex-smallest).
	got1 := flattenGroups(mergeAbsenceRuleGroups("thanos-b",
		[]monitoringv1.RuleGroup{mkRuleGroup("thanos-a/g", dup, expr)},
		[]monitoringv1.RuleGroup{mkRuleGroup("thanos-b/g", dup, expr)},
	))

	// Case 2: thanos-a reconciling; thanos-b already in existing.
	// Winner must still be thanos-a.
	got2 := flattenGroups(mergeAbsenceRuleGroups("thanos-a",
		[]monitoringv1.RuleGroup{mkRuleGroup("thanos-b/g", dup, expr)},
		[]monitoringv1.RuleGroup{mkRuleGroup("thanos-a/g", dup, expr)},
	))

	for _, got := range [][][3]string{got1, got2} {
		if len(got) != 1 || got[0][0] != "thanos-a/g" {
			t.Fatalf("expected winner thanos-a/g, got %#v", got)
		}
	}
}

func TestMergeAbsenceRuleGroups_ReplacesCurrentPRStaleGroups(t *testing.T) {
	// Fresh groups for the current source PR must replace, not augment,
	// any groups that PR previously contributed.
	existing := []monitoringv1.RuleGroup{
		mkRuleGroup("thanos-a/old", "OldAlert", "absent(old_metric)"),
		mkRuleGroup("thanos-b/keep", "KeepAlert", "absent(other_metric)"),
	}
	fresh := []monitoringv1.RuleGroup{
		mkRuleGroup("thanos-a/new", "NewAlert", "absent(new_metric)"),
	}

	got := flattenGroups(mergeAbsenceRuleGroups("thanos-a", existing, fresh))
	// Sorted by group name: thanos-a/new, thanos-b/keep. thanos-a/old must be gone.
	want := [][3]string{
		{"thanos-a/new", "NewAlert", "absent(new_metric)"},
		{"thanos-b/keep", "KeepAlert", "absent(other_metric)"},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d rules, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rule %d mismatch:\n got:  %#v\n want: %#v", i, got[i], want[i])
		}
	}
}

func TestMergeAbsenceRuleGroups_PrefixMatchIsSlashBounded(t *testing.T) {
	// A source PR named "example-thanos" must not strip groups belonging to
	// an unrelated PR "example-thanos-v2". Without the trailing-"/" bound,
	// strings.HasPrefix would incorrectly match both.
	existing := []monitoringv1.RuleGroup{
		mkRuleGroup("example-thanos-v2/g", "V2Alert", "absent(v2_metric)"),
	}
	got := flattenGroups(mergeAbsenceRuleGroups("example-thanos", existing, nil))
	if len(got) != 1 || got[0][0] != "example-thanos-v2/g" {
		t.Fatalf("v2 group was incorrectly stripped: %#v", got)
	}
}

func TestMergeAbsenceRuleGroups_DropsGroupsThatBecomeEmpty(t *testing.T) {
	// If dedup consumes every rule in a group, the group itself must be
	// dropped rather than left behind as an empty stub — an empty group
	// serialises to invalid PrometheusRule YAML.
	dup := "AbsentContainersKubeNodeStatusCondition"
	expr := "absent(kube_node_status_condition)"
	existing := []monitoringv1.RuleGroup{
		mkRuleGroup("thanos-a/g", dup, expr),
	}
	fresh := []monitoringv1.RuleGroup{
		mkRuleGroup("thanos-b/only-dup", dup, expr),
	}

	out := mergeAbsenceRuleGroups("thanos-b", existing, fresh)
	if len(out) != 1 || out[0].Name != "thanos-a/g" {
		t.Fatalf("expected only thanos-a/g to remain, got %#v", flattenGroups(out))
	}
}

func TestCreateAbsencePromRuleNameGenerator_EmptyTemplateOutput(t *testing.T) {
	// The default name template reads .metadata.labels.thanos-ruler /
	// .metadata.labels.prometheus. A source PR that sets neither renders
	// the template to "", producing an RFC1123-invalid CR name like
	// "-absent-metric-alert-rules". The generator must fail with the
	// sentinel error so the reconciler can log-and-skip instead of
	// thrashing.
	gen, err := CreateAbsencePromRuleNameGenerator(DefaultAbsencePromRuleNameTemplate)
	if err != nil {
		t.Fatalf("unexpected generator construction error: %v", err)
	}
	pr := &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-thanos-rules",
			Namespace: "kube-monitoring",
			// Deliberately no thanos-ruler / prometheus labels.
		},
	}
	name, err := gen(pr)
	if err == nil {
		t.Fatalf("expected error, got name=%q", name)
	}
	if !errors.Is(err, errEmptyAbsencePromRuleName) {
		t.Fatalf("expected errEmptyAbsencePromRuleName, got %v", err)
	}
}
