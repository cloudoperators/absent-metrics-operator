// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

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
// When nil (or empty), the feature is disabled and absent() is always generated
// without any label matchers (the existing behaviour).
type AbsentLabel map[string]bool
