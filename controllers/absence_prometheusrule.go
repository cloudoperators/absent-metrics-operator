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
	DefaultAbsencePromRuleNameTemplate = `{{ if index .metadata.labels "thanos-ruler" }}{{ index .metadata.labels "thanos-ruler" }}{{ else }}{{ index .metadata.labels "prometheus" }}{{ end }}`
)

// AbsencePromRuleNameGenerator is a function type that takes a PrometheusRule and
// generates a name for its corresponding PrometheusRule that holds the generated absence
// alert rules.
type AbsencePromRuleNameGenerator func(*monitoringv1.PrometheusRule) (string, error)

// CreateAbsencePromRuleNameGenerator creates an absencePromRuleNameGenerator function
// based on a template string.
func CreateAbsencePromRuleNameGenerator(tmplStr string) (AbsencePromRuleNameGenerator, error) {
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

		return buf.String() + absencePromRuleNameSuffix, nil
	}, nil
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
func (r *PrometheusRuleReconciler) cleanUpOrphanedAbsenceAlertRules(
	ctx context.Context,
	promRule types.NamespacedName,
	absencePromRule string,
) error {

	// Step 1: find the corresponding AbsencePrometheusRule that needs to be cleaned up.
	var aPRToClean *monitoringv1.PrometheusRule
	if absencePromRule != "" {
		var err error
		if aPRToClean, err = r.getExistingAbsencePrometheusRule(ctx, absencePromRule, promRule.Namespace); err != nil {
			return err
		}
	} else {
		// Since we don't know the corresponding AbsencePrometheusRule for this PrometheusRule
		// therefore we have to list all AbsencePrometheusRules in the concerning namespace and
		// find the specific AbsencePrometheusRule that contains the absence alert rules that
		// were generated for this PrometheusRule.
		var listOpts client.ListOptions
		client.InNamespace(promRule.Namespace).ApplyToList(&listOpts)
		client.HasLabels{labelOperatorManagedBy}.ApplyToList(&listOpts)
		var absencePromRules monitoringv1.PrometheusRuleList
		if err := r.List(ctx, &absencePromRules, &listOpts); err != nil {
			return err
		}

		for _, aPR := range absencePromRules.Items {
			for _, g := range aPR.Spec.Groups {
				n := promRulefromAbsenceRuleGroupName(g.Name)
				if n != "" && n == promRule.Name {
					aPRToClean = &aPR
					break
				}
			}
		}
	}
	if aPRToClean == nil {
		return errCorrespondingAbsencePromRuleNotExists
	}

	// Step 2: iterate through the AbsenceRuleGroups, skip those that were generated for
	// this PrometheusRule and keep the rest as is.
	oldRuleGroups := aPRToClean.Spec.Groups
	newRuleGroups := make([]monitoringv1.RuleGroup, 0, len(oldRuleGroups))
	for _, g := range oldRuleGroups {
		n := promRulefromAbsenceRuleGroupName(g.Name)
		if n != "" && n == promRule.Name {
			continue
		}
		newRuleGroups = append(newRuleGroups, g)
	}
	if reflect.DeepEqual(oldRuleGroups, newRuleGroups) {
		return nil
	}

	// Step 3: if, after the cleanup, the AbsencePrometheusRule ends up being empty then
	// delete it otherwise update.
	if len(newRuleGroups) == 0 {
		return r.deleteAbsencePrometheusRule(ctx, aPRToClean)
	}
	unmodified := aPRToClean.DeepCopy()
	aPRToClean.Spec.Groups = newRuleGroups
	return r.patchAbsencePrometheusRule(ctx, aPRToClean, unmodified)
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
	aPRName := absencePromRule.GetName()
	prNames := make(map[string]bool)
	for _, pr := range promRules.Items {
		if _, ok := pr.Labels[labelOperatorManagedBy]; ok {
			continue
		}
		if n, err := r.PrometheusRuleName(&pr); err == nil {
			if n == aPRName {
				prNames[pr.GetName()] = true
			}
		}
	}

	// Step 4: iterate through all the AbsencePrometheusRule's RuleGroups and remove those
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

	// Step 5: if, after the cleanup, the AbsencePrometheusRule ends up being empty then
	// delete it otherwise update.
	if len(newRuleGroups) == 0 {
		return r.deleteAbsencePrometheusRule(ctx, absencePromRule)
	}
	unmodified := absencePromRule.DeepCopy()
	absencePromRule.Spec.Groups = newRuleGroups
	return r.patchAbsencePrometheusRule(ctx, absencePromRule, unmodified)
}

// updateAbsenceAlertRules generates absence alert rules for the given PrometheusRule and
// adds them to the corresponding AbsencePrometheusRule.
func (r *PrometheusRuleReconciler) updateAbsenceAlertRules(ctx context.Context, promRule *monitoringv1.PrometheusRule) error {
	promRuleName := promRule.GetName()
	namespace := promRule.GetNamespace()
	log := r.Log.WithValues("name", promRuleName, "namespace", namespace)

	// Step 1: get the corresponding AbsencePrometheusRule if it exists.
	existingAbsencePrometheusRule := false
	aPRName, err := r.PrometheusRuleName(promRule)
	if err != nil {
		return err
	}
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

	// Step 2: parse RuleGroups and generate corresponding absence alert rules.
	absenceRuleGroups, err := ParseRuleGroups(log, promRule.Spec.Groups, promRuleName, r.KeepLabel)
	if err != nil {
		return err
	}

	// Step 3: we clean up orphaned absence alert rules from the AbsencePrometheusRule in
	// case no absence alert rules were generated.
	// This can happen when changes have been made to alert rules that result in no absent
	// alerts. E.g. absent() or the 'no_alert_on_absence' label was used.
	if len(absenceRuleGroups) == 0 {
		if existingAbsencePrometheusRule {
			key := types.NamespacedName{Namespace: namespace, Name: promRuleName}
			return r.cleanUpOrphanedAbsenceAlertRules(ctx, key, aPRName)
		}
		return nil
	}

	// Step 4: if it's an existing AbsencePrometheusRule then update otherwise create a new resource.
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

// mergeAbsenceRuleGroups merges existing and newly generated AbsenceRuleGroups. If the
// same AbsenceRuleGroup exists in both 'existing' and 'new' then the newer one will be
// used.
func mergeAbsenceRuleGroups(promRuleName string, existingRuleGroups, newRuleGroups []monitoringv1.RuleGroup) []monitoringv1.RuleGroup {
	var result []monitoringv1.RuleGroup
	// Add the absence rule groups for the PrometheusRule that we are currently dealing with.
	result = append(result, newRuleGroups...)
	// Carry over the absence rule groups for other PrometheusRule(s) as is.
	for _, g := range existingRuleGroups {
		if !strings.HasPrefix(g.Name, promRuleName) {
			result = append(result, g)
		}
	}
	return result
}
