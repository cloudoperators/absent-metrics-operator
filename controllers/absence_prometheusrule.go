// SPDX-FileCopyrightText: 2020 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"text/template"
	"time"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	absencePromRuleNameSuffix          = "-absent-metric-alert-rules"
	labeledAbsencePromRuleNameSuffix   = "-absent-metric-labeled-alert-rules"
	DefaultAbsencePromRuleNameTemplate = `{{ if index .metadata.labels "thanos-ruler" }}{{ index .metadata.labels "thanos-ruler" }}{{ else }}{{ index .metadata.labels "prometheus" }}{{ end }}`
)

// AbsencePromRuleNameGenerator is a function type that takes a PrometheusRule and
// generates a name for its corresponding PrometheusRule that holds the generated absence
// alert rules.
type AbsencePromRuleNameGenerator func(*monitoringv1.PrometheusRule) (string, error)

// CreateAbsencePromRuleNameGenerator creates an AbsencePromRuleNameGenerator for the
// bare absence rules CR (one absent(metric_name) per metric), based on a template string.
func CreateAbsencePromRuleNameGenerator(tmplStr string) (AbsencePromRuleNameGenerator, error) {
	return createAbsencePromRuleNameGenerator(tmplStr, absencePromRuleNameSuffix)
}

// CreateLabeledAbsencePromRuleNameGenerator creates an AbsencePromRuleNameGenerator for
// the labeled absence rules CR (one absent(metric_name{label="value",...}) per distinct
// label-value combo), based on a template string. The generated name uses a distinct
// suffix so the labeled CR sits next to the bare CR rather than colliding with it.
func CreateLabeledAbsencePromRuleNameGenerator(tmplStr string) (AbsencePromRuleNameGenerator, error) {
	return createAbsencePromRuleNameGenerator(tmplStr, labeledAbsencePromRuleNameSuffix)
}

// errEmptyAbsencePromRuleName is returned by the name generator when the
// user-supplied template renders to an empty string (typically because the
// source PrometheusRule is missing the labels the template reads). It is
// sentinel so callers can log-and-skip instead of failing the reconcile,
// which would otherwise thrash forever on a PR that will never satisfy the
// template.
var errEmptyAbsencePromRuleName = errors.New("AbsencePrometheusRule name template rendered to an empty string")

// createAbsencePromRuleNameGenerator is the shared template-driven name generator used
// by both the bare and labeled flavours. The two public constructors above differ only
// in the suffix they pass in.
func createAbsencePromRuleNameGenerator(tmplStr, suffix string) (AbsencePromRuleNameGenerator, error) {
	t, err := template.New("promRuleNameGenerator").Option("missingkey=error").Parse(tmplStr)
	if err != nil {
		return nil, err
	}

	return func(pr *monitoringv1.PrometheusRule) (string, error) {
		// only a specific vetted subset of attributes is passed into the name template to avoid surprising behavior
		meta := pr.ObjectMeta
		data := map[string]any{
			"metadata": map[string]any{
				"annotations": meta.Annotations,
				"labels":      meta.Labels,
				"namespace":   meta.Namespace,
				"name":        meta.Name,
			},
		}

		var buf bytes.Buffer
		err = t.Execute(&buf, data)
		if err != nil {
			return "", fmt.Errorf("could not generate AbsencePrometheusRule name: %w", err)
		}

		// Reject empty template output. Without this guard the returned name
		// is just the suffix ("-absent-metric-alert-rules"), which fails
		// Kubernetes' RFC 1123 subdomain validation ("must start and end
		// with an alphanumeric character") and every reconcile of a
		// PrometheusRule that doesn't set the label(s) the template reads
		// (e.g. neither "thanos-ruler" nor "prometheus" on the default
		// template) fails with an "Invalid value" API error.
		prefix := buf.String()
		if prefix == "" {
			return "", fmt.Errorf("%w for %s/%s; the PrometheusRule is missing the labels the template reads",
				errEmptyAbsencePromRuleName, pr.GetNamespace(), pr.GetName())
		}
		return prefix + suffix, nil
	}, nil
}

// isAbsencePromRuleName reports whether name looks like one of the absence CR names this
// operator generates (either bare or labeled flavour). Used by handleObjectNotFound to
// short-circuit reconciliation when a managed CR is itself deleted.
func isAbsencePromRuleName(name string) bool {
	return strings.HasSuffix(name, absencePromRuleNameSuffix) ||
		strings.HasSuffix(name, labeledAbsencePromRuleNameSuffix)
}

func (r *PrometheusRuleReconciler) newAbsencePrometheusRule(name, namespace string, labels map[string]string) *monitoringv1.PrometheusRule {
	l := map[string]string{
		// Add a label that identifies that this PrometheusRule resource is
		// created and managed by this operator.
		labelOperatorManagedBy: "true",
		"type":                 "alerting-rules",
	}
	// Carry over labels from source PrometheusRule object if needed.
	if v, ok := labels[labelPrometheusServer]; ok {
		l[labelPrometheusServer] = v
	}
	if v, ok := labels[labelGreenhousePlugin]; ok {
		l[labelGreenhousePlugin] = v
	}
	if v, ok := labels[labelThanosRuler]; ok {
		l[labelThanosRuler] = v
	}

	return &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    l,
		},
	}
}

func (r *PrometheusRuleReconciler) getExistingAbsencePrometheusRule(
	ctx context.Context,
	name, namespace string,
) (*monitoringv1.PrometheusRule, error) {

	var absencePromRule monitoringv1.PrometheusRule
	nsName := types.NamespacedName{Namespace: namespace, Name: name}
	if err := r.Get(ctx, nsName, &absencePromRule); err != nil {
		return nil, err
	}
	return &absencePromRule, nil
}

func sortRuleGroups(absencePromRule *monitoringv1.PrometheusRule) {
	// Sort rule groups for consistent test results.
	sort.SliceStable(absencePromRule.Spec.Groups, func(i, j int) bool {
		return absencePromRule.Spec.Groups[i].Name < absencePromRule.Spec.Groups[j].Name
	})
}

func updateAnnotationTime(absencePromRule *monitoringv1.PrometheusRule) {
	now := time.Now()
	if IsTest {
		now = time.Unix(1, 0)
	}
	if absencePromRule.Annotations == nil {
		absencePromRule.Annotations = make(map[string]string)
	}
	absencePromRule.Annotations[annotationOperatorUpdatedAt] = now.UTC().Format(time.RFC3339)
}

func (r *PrometheusRuleReconciler) createAbsencePrometheusRule(ctx context.Context, absencePromRule *monitoringv1.PrometheusRule) error {
	sortRuleGroups(absencePromRule)
	updateAnnotationTime(absencePromRule)
	if err := r.Create(ctx, absencePromRule); err != nil {
		return err
	}

	r.Log.V(logLevelDebug).Info("successfully created AbsencePrometheusRule",
		"AbsencePrometheusRule", fmt.Sprintf("%s/%s", absencePromRule.GetNamespace(), absencePromRule.GetName()))
	return nil
}

func (r *PrometheusRuleReconciler) patchAbsencePrometheusRule(
	ctx context.Context,
	absencePromRule,
	unmodifiedAbsencePromRule *monitoringv1.PrometheusRule,
) error {

	sortRuleGroups(absencePromRule)
	updateAnnotationTime(absencePromRule)
	if err := r.Patch(ctx, absencePromRule, client.MergeFrom(unmodifiedAbsencePromRule)); err != nil {
		return err
	}

	r.Log.V(logLevelDebug).Info("successfully updated AbsencePrometheusRule",
		"AbsencePrometheusRule", fmt.Sprintf("%s/%s", absencePromRule.GetNamespace(), absencePromRule.GetName()))
	return nil
}

func (r *PrometheusRuleReconciler) deleteAbsencePrometheusRule(ctx context.Context, absencePromRule *monitoringv1.PrometheusRule) error {
	if err := r.Delete(ctx, absencePromRule); err != nil {
		return err
	}

	r.Log.V(logLevelDebug).Info("successfully deleted AbsencePrometheusRule",
		"AbsencePrometheusRule", fmt.Sprintf("%s/%s", absencePromRule.GetNamespace(), absencePromRule.GetName()))
	return nil
}

var errCorrespondingAbsencePromRuleNotExists = errors.New("corresponding AbsencePrometheusRule for clean up does not exist")

// cleanUpOrphanedAbsenceAlertRules deletes the absence alert rules for a PrometheusRule
// resource.
//
// We use this when a PrometheusRule resource has been deleted or if the
// 'absent-metrics-operator/disable' is set to 'true'.
//
// When absencePromRule is non-empty, the targeted AbsencePrometheusRule is fetched by
// name and scrubbed. When absencePromRule is empty, every managed AbsencePrometheusRule
// in the namespace is inspected and any that holds groups belonging to this source
// PrometheusRule is scrubbed — important now that one source can produce both a bare
// CR and a labeled CR; both must be cleaned up.
func (r *PrometheusRuleReconciler) cleanUpOrphanedAbsenceAlertRules(
	ctx context.Context,
	promRule types.NamespacedName,
	absencePromRule string,
) error {

	if absencePromRule != "" {
		aPRToClean, err := r.getExistingAbsencePrometheusRule(ctx, absencePromRule, promRule.Namespace)
		if err != nil {
			return err
		}
		return r.applyScrubToAbsencePR(ctx, aPRToClean, promRule.Name)
	}

	// List-mode: walk every managed AbsencePrometheusRule in the namespace and scrub
	// any that owns groups for this source PrometheusRule. We do NOT break after the
	// first match: a single source can land in two CRs (bare + labeled) and both
	// must be cleaned up.
	var listOpts client.ListOptions
	client.InNamespace(promRule.Namespace).ApplyToList(&listOpts)
	client.HasLabels{labelOperatorManagedBy}.ApplyToList(&listOpts)
	var absencePromRules monitoringv1.PrometheusRuleList
	if err := r.List(ctx, &absencePromRules, &listOpts); err != nil {
		return err
	}

	scrubbed := false
	for i := range absencePromRules.Items {
		aPR := &absencePromRules.Items[i]
		if !absencePRContainsGroupsFor(aPR, promRule.Name) {
			continue
		}
		if err := r.applyScrubToAbsencePR(ctx, aPR, promRule.Name); err != nil {
			return err
		}
		scrubbed = true
	}
	if !scrubbed {
		return errCorrespondingAbsencePromRuleNotExists
	}
	return nil
}

// absencePRContainsGroupsFor reports whether an AbsencePrometheusRule has any rule
// group attributable to the named source PrometheusRule (i.e. whose group name encodes
// that source via the "<srcName>/<originalGroup>" convention).
func absencePRContainsGroupsFor(aPR *monitoringv1.PrometheusRule, sourceName string) bool {
	for _, g := range aPR.Spec.Groups {
		if promRulefromAbsenceRuleGroupName(g.Name) == sourceName {
			return true
		}
	}
	return false
}

// applyScrubToAbsencePR removes every rule group attributable to sourceName from aPR
// and persists the result. When all groups belong to sourceName the CR is deleted
// outright; otherwise it is patched. Returns nil when no change is needed.
func (r *PrometheusRuleReconciler) applyScrubToAbsencePR(
	ctx context.Context,
	aPR *monitoringv1.PrometheusRule,
	sourceName string,
) error {

	oldRuleGroups := aPR.Spec.Groups
	newRuleGroups := make([]monitoringv1.RuleGroup, 0, len(oldRuleGroups))
	for _, g := range oldRuleGroups {
		if promRulefromAbsenceRuleGroupName(g.Name) == sourceName {
			continue
		}
		newRuleGroups = append(newRuleGroups, g)
	}
	if reflect.DeepEqual(oldRuleGroups, newRuleGroups) {
		return nil
	}
	if len(newRuleGroups) == 0 {
		return r.deleteAbsencePrometheusRule(ctx, aPR)
	}
	unmodified := aPR.DeepCopy()
	aPR.Spec.Groups = newRuleGroups
	return r.patchAbsencePrometheusRule(ctx, aPR, unmodified)
}

// cleanUpAllAbsencePromRulesFor cleans up both the bare AbsencePrometheusRule and the
// labeled AbsencePrometheusRule for a source PrometheusRule whose disable label was
// flipped on. Each is targeted by name (fast Get path); errCorrespondingAbsencePromRuleNotExists
// from any single one is treated as "nothing to do for that flavour" and not propagated.
// All other errors are returned (first one wins) so the caller can decide whether to retry.
func (r *PrometheusRuleReconciler) cleanUpAllAbsencePromRulesFor(
	ctx context.Context,
	source *monitoringv1.PrometheusRule,
	key types.NamespacedName,
) error {

	generators := []AbsencePromRuleNameGenerator{r.PrometheusRuleName, r.LabeledPrometheusRuleName}
	var firstErr error
	for _, gen := range generators {
		if gen == nil {
			continue
		}
		aPRName, err := gen(source)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		err = r.cleanUpOrphanedAbsenceAlertRules(ctx, key, aPRName)
		if err != nil && !apierrors.IsNotFound(err) && !errors.Is(err, errCorrespondingAbsencePromRuleNotExists) {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// cleanUpAbsencePrometheusRule checks an AbsencePrometheusRule to see if it contains
// absence alert rules for a PrometheusRule that no longer exists or for a resource that
// has the 'absent-metrics-operator/disable' label. If such rules are found then they are
// deleted.
func (r *PrometheusRuleReconciler) cleanUpAbsencePrometheusRule(ctx context.Context, absencePromRule *monitoringv1.PrometheusRule) error {
	// Step 1: get names of all PrometheusRule resources in this namespace.
	var listOpts client.ListOptions
	client.InNamespace(absencePromRule.GetNamespace()).ApplyToList(&listOpts)
	var promRules monitoringv1.PrometheusRuleList
	if err := r.List(ctx, &promRules, &listOpts); err != nil {
		return err
	}

	// Step 2: collect names of those PrometheusRule resources whose absence alert rules
	// would end up in this AbsencePrometheusRule as per the name generation template.
	// A source PR is a match if EITHER its bare CR name OR its labeled CR name (when
	// the labeled feature is enabled) equals the CR we're scrubbing.
	aPRName := absencePromRule.GetName()
	generators := []AbsencePromRuleNameGenerator{r.PrometheusRuleName, r.LabeledPrometheusRuleName}
	prNames := make(map[string]bool)
	for _, pr := range promRules.Items {
		if _, ok := pr.Labels[labelOperatorManagedBy]; ok {
			continue
		}
		for _, gen := range generators {
			if gen == nil {
				continue
			}
			if n, err := gen(&pr); err == nil && n == aPRName {
				prNames[pr.GetName()] = true
				break
			}
		}
	}

	// Step 3: iterate through all the AbsencePrometheusRule's RuleGroups and remove those
	// that don't belong to any PrometheusRule.
	newRuleGroups := make([]monitoringv1.RuleGroup, 0, len(absencePromRule.Spec.Groups))
	for _, g := range absencePromRule.Spec.Groups {
		n := promRulefromAbsenceRuleGroupName(g.Name)
		if !prNames[n] {
			continue
		}
		newRuleGroups = append(newRuleGroups, g)
	}
	if reflect.DeepEqual(absencePromRule.Spec.Groups, newRuleGroups) {
		return nil
	}

	// Step 4: if, after the cleanup, the AbsencePrometheusRule ends up being empty then
	// delete it otherwise update.
	if len(newRuleGroups) == 0 {
		return r.deleteAbsencePrometheusRule(ctx, absencePromRule)
	}
	unmodified := absencePromRule.DeepCopy()
	absencePromRule.Spec.Groups = newRuleGroups
	return r.patchAbsencePrometheusRule(ctx, absencePromRule, unmodified)
}

// updateAbsenceAlertRules generates absence alert rules for the given PrometheusRule and
// adds them to the corresponding AbsencePrometheusRule(s).
//
// When AbsentLabel is non-empty and LabeledPrometheusRuleName is configured, bare and
// labeled absence rules land in two SEPARATE AbsencePrometheusRule CRs (one per
// flavour). When the labeled feature is off (either AbsentLabel empty or
// LabeledPrometheusRuleName nil) only the bare CR is reconciled — which is the
// pre-feature behaviour.
func (r *PrometheusRuleReconciler) updateAbsenceAlertRules(ctx context.Context, promRule *monitoringv1.PrometheusRule) error {
	promRuleName := promRule.GetName()
	namespace := promRule.GetNamespace()
	log := r.Log.WithValues("name", promRuleName, "namespace", namespace)

	// Parse the source rule groups once into the two output streams.
	bareGroups, labeledGroups, err := ParseRuleGroups(log, promRule.Spec.Groups, promRuleName, r.KeepLabel, r.AbsentLabel)
	if err != nil {
		return err
	}

	// Reconcile the bare CR.
	bareName, err := r.PrometheusRuleName(promRule)
	if err != nil {
		return err
	}
	if err := r.reconcileOneAbsenceCR(ctx, promRule, bareName, bareGroups); err != nil {
		return err
	}

	// Reconcile the labeled CR only when the feature is enabled. We deliberately
	// always make this call (even when labeledGroups is nil) so that switching
	// AbsentLabel off after a previous on-cycle still triggers cleanup of the
	// labeled CR via the "no new groups → scrub existing" path inside
	// reconcileOneAbsenceCR.
	if r.LabeledPrometheusRuleName != nil {
		labeledName, err := r.LabeledPrometheusRuleName(promRule)
		if err != nil {
			return err
		}
		if err := r.reconcileOneAbsenceCR(ctx, promRule, labeledName, labeledGroups); err != nil {
			return err
		}
	}
	return nil
}

// reconcileOneAbsenceCR reconciles a single AbsencePrometheusRule CR for one source
// PrometheusRule and one flavour of absence rules (bare or labeled). It encapsulates
// the get-or-create / merge / patch / orphan-cleanup state machine that used to live
// inline in updateAbsenceAlertRules, and is now invoked once for the bare CR and once
// for the labeled CR.
func (r *PrometheusRuleReconciler) reconcileOneAbsenceCR(
	ctx context.Context,
	promRule *monitoringv1.PrometheusRule,
	aPRName string,
	absenceRuleGroups []monitoringv1.RuleGroup,
) error {

	promRuleName := promRule.GetName()
	namespace := promRule.GetNamespace()

	// Step 1: get the corresponding AbsencePrometheusRule if it exists.
	existingAbsencePrometheusRule := false
	absencePromRule, err := r.getExistingAbsencePrometheusRule(ctx, aPRName, namespace)
	switch {
	case err == nil:
		existingAbsencePrometheusRule = true
	case apierrors.IsNotFound(err):
		absencePromRule = r.newAbsencePrometheusRule(aPRName, namespace, promRule.GetLabels())
	default:
		// This could have been caused by a temporary network failure, or any
		// other transient reason.
		return err
	}

	unmodifiedAbsencePromRule := absencePromRule.DeepCopy()

	// Step 2: if there are no rule groups for this flavour then clean up any orphans.
	// This can happen when changes have been made to alert rules that result in no
	// absent alerts (e.g. absent() or the 'no_alert_on_absence' label was used), or
	// when the labeled feature was just turned off and the labeled CR needs to be
	// emptied.
	if len(absenceRuleGroups) == 0 {
		if existingAbsencePrometheusRule {
			key := types.NamespacedName{Namespace: namespace, Name: promRuleName}
			err := r.cleanUpOrphanedAbsenceAlertRules(ctx, key, aPRName)
			if errors.Is(err, errCorrespondingAbsencePromRuleNotExists) {
				return nil
			}
			return err
		}
		return nil
	}

	// Step 3: if it's an existing AbsencePrometheusRule then update otherwise create a new resource.
	if existingAbsencePrometheusRule {
		existingRuleGroups := unmodifiedAbsencePromRule.Spec.Groups
		result := mergeAbsenceRuleGroups(promRuleName, existingRuleGroups, absenceRuleGroups)
		if reflect.DeepEqual(unmodifiedAbsencePromRule.GetLabels(), absencePromRule.GetLabels()) &&
			reflect.DeepEqual(existingRuleGroups, result) {
			return nil
		}
		absencePromRule.Spec.Groups = result
		return r.patchAbsencePrometheusRule(ctx, absencePromRule, unmodifiedAbsencePromRule)
	}
	absencePromRule.Spec.Groups = absenceRuleGroups
	return r.createAbsencePrometheusRule(ctx, absencePromRule)
}

// mergeAbsenceRuleGroups produces the full set of absence rule groups that should
// live in an AbsencePrometheusRule after reconciling one source PrometheusRule.
//
// It combines newRuleGroups (freshly generated for the source PR named promRuleName)
// with any groups already stored in existingRuleGroups that belong to OTHER source
// PRs, then deduplicates identical absent() rules that span multiple source PRs.
//
// Two source PRs can reference the same metric — e.g. several thanos-tagged
// PrometheusRules each carrying an alert on kube_node_status_condition — and each
// contributes its own group named "<sourcePR>/<originalGroup>" to the aggregated
// CR. Without cross-source dedup the CR ends up with several identical
// `absent(kube_node_status_condition)` entries, one per source PR.
//
// Dedup key is (Alert, Expr). To keep the resulting CR content independent of
// reconciliation order (so different source PRs don't ping-pong duplicates in and
// out of the CR on every reconcile), the winner for each (Alert, Expr) pair is
// picked deterministically: the group whose name sorts lex-smallest wins, which
// — because group names are "<sourcePR>/<originalGroup>" — effectively picks the
// lex-smallest source PR. Groups that end up empty after dedup are dropped.
func mergeAbsenceRuleGroups(promRuleName string, existingRuleGroups, newRuleGroups []monitoringv1.RuleGroup) []monitoringv1.RuleGroup {
	// Strip the current source PR's stale groups from existing (they are being
	// replaced by newRuleGroups) and keep every group that belongs to other
	// source PRs. The trailing "/" is significant: without it a PR named
	// "example-thanos-rules" would also match groups from an unrelated
	// "example-thanos-rules-v2".
	prefix := promRuleName + "/"
	var merged []monitoringv1.RuleGroup
	merged = append(merged, newRuleGroups...)
	for _, g := range existingRuleGroups {
		if !strings.HasPrefix(g.Name, prefix) {
			merged = append(merged, g)
		}
	}

	// Sort groups by name so the (Alert, Expr) dedup below always sees the
	// same iteration order regardless of which source PR triggered this
	// reconcile — that's what makes the resulting CR content stable.
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].Name < merged[j].Name
	})

	seen := make(map[string]bool)
	deduped := make([]monitoringv1.RuleGroup, 0, len(merged))
	for _, g := range merged {
		filtered := make([]monitoringv1.Rule, 0, len(g.Rules))
		for _, r := range g.Rules {
			key := r.Alert + "\x00" + r.Expr.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			filtered = append(filtered, r)
		}
		if len(filtered) > 0 {
			g.Rules = filtered
			deduped = append(deduped, g)
		}
	}
	return deduped
}
