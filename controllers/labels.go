// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

import "strings"

// These constants are exported for reusability across packages.
const (
	LabelSupportGroup = "support_group"
	LabelTier         = "tier"
	LabelService      = "service"
)

const (
	annotationOperatorUpdatedAt = "absent-metrics-operator/updated-at"

	labelOperatorManagedBy = "absent-metrics-operator/managed-by"
	labelOperatorDisable   = "absent-metrics-operator/disable"

	labelNoAlertOnAbsence = "no_alert_on_absence"
	labelPrometheusServer = "prometheus"
	labelGreenhousePlugin = "plugin"
	labelThanosRuler      = "thanos-ruler"
)

// KeepLabel specifies which labels to keep on an absence alert rule.
type KeepLabel map[string]bool

// AbsentLabel specifies which label names, if present in a metric's selector
// expression, should be included in the generated absent() call.
//
// Each key is a pattern, not just a literal label name. Supported syntax:
//
//   - "name"          — exact match (backwards-compatible)
//   - "*"             — match every label name
//   - "prefix_*"      — prefix wildcard (matches "prefix_foo", "prefix_bar")
//   - "*_suffix"      — suffix wildcard
//   - "*middle*"      — contains
//
// The internal label name "__name__" is never matched by any pattern.
//
// When nil (or empty), the feature is disabled and absent() is always
// generated without any label matchers (the existing behaviour).
type AbsentLabel map[string]bool

// Matches reports whether the given label name is selected by any of the
// patterns in the AbsentLabel set. The internal "__name__" label is never
// matched.
func (al AbsentLabel) Matches(labelName string) bool {
	if labelName == "" || labelName == "__name__" {
		return false
	}
	for pattern := range al {
		if matchLabelPattern(pattern, labelName) {
			return true
		}
	}
	return false
}

// matchLabelPattern matches a single pattern against a label name. The
// pattern may contain '*' wildcards at the start, end, or both. A pattern
// with no '*' is treated as an exact match.
func matchLabelPattern(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	if !strings.ContainsRune(pattern, '*') {
		return pattern == name
	}
	if pattern == "*" {
		return true
	}
	hasPrefix := strings.HasPrefix(pattern, "*")
	hasSuffix := strings.HasSuffix(pattern, "*")
	core := strings.Trim(pattern, "*")
	// A pattern made entirely of '*' (e.g. "**") behaves like "*".
	if core == "" {
		return true
	}
	switch {
	case hasPrefix && hasSuffix:
		return strings.Contains(name, core)
	case hasPrefix:
		return strings.HasSuffix(name, core)
	case hasSuffix:
		return strings.HasPrefix(name, core)
	default:
		// Middle-only wildcards (e.g. "foo*bar") are not supported.
		// Returning false is safe because a pattern still containing '*' can
		// never equal a real label name.
		return false
	}
}
