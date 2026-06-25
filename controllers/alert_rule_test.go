// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var _ = Describe("Alert Rule", func() {
	logger := zap.New(zap.UseDevMode(true))
	keepLabel := KeepLabel{
		LabelSupportGroup: true,
		LabelTier:         true,
		LabelService:      true,
	}

	DescribeTable("Parsing alert rule expressions",
		func(in monitoringv1.Rule, out []monitoringv1.Rule) {
			expected := out
			actual, err := parseRule(logger, in, keepLabel, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(actual).To(HaveLen(len(expected)))

			// We only check the alert name, expression, and labels. Annotations are hard-coded and
			// don't need to be checked here in unit tests; they are already checked in e2e tests.
			for i, wanted := range expected {
				got := actual[i]
				Expect(got.Alert).To(Equal(wanted.Alert))
				Expect(got.Expr).To(Equal(wanted.Expr))
				Expect(got.Labels).To(Equal(wanted.Labels))
			}
		},
		Entry("alert rule with label matching in expression and templating in labels",
			monitoringv1.Rule{
				Alert: "OpenstackKeppelPodSchedulingInsufficientMemory",
				Expr:  intstr.FromString(`sum(rate(kube_pod_failed_scheduling_memory_total{namespace="keppel"}[30m])) by (pod_name) > 0`),
				Labels: map[string]string{
					"tier":    `{{ $labels.somelabel }}`,
					"service": "keppel",
				},
			},
			[]monitoringv1.Rule{{
				Alert: "AbsentKeppelKubePodFailedSchedulingMemoryTotal",
				Expr:  intstr.FromString(`absent(kube_pod_failed_scheduling_memory_total)`),
				Labels: map[string]string{
					"context":  "absent-metrics",
					"severity": "info",
					"service":  "keppel",
				},
			}},
		),
		Entry("alert rule with multiple usage of the same metric in expression",
			monitoringv1.Rule{
				Alert: "OpenstackKeppelPodOOMExceedingLimits",
				Expr:  intstr.FromString(`keppel_container_memory_usage_percent > 70 and predict_linear(keppel_container_memory_usage_percent[1h], 7*3600) > 100`),
				Labels: map[string]string{
					"tier":    "os",
					"service": "keppel",
				},
			},
			[]monitoringv1.Rule{{
				Alert: "AbsentOsKeppelContainerMemoryUsagePercent",
				Expr:  intstr.FromString(`absent(keppel_container_memory_usage_percent)`),
				Labels: map[string]string{
					"context":  "absent-metrics",
					"severity": "info",
					"tier":     "os",
					"service":  "keppel",
				},
			}},
		),
		Entry("alert rule with multiple different metrics in the expression",
			monitoringv1.Rule{
				Alert: "OpenstackSwiftHealthCheck",
				Expr:  intstr.FromString(`avg(swift_recon_task_exit_code) BY (region) > 0.2 or avg(swift_dispersion_task_exit_code) BY (region) > 0.2`),
				Labels: map[string]string{
					"support_group": "not-containers",
					"tier":          "os",
					"service":       "swift",
				},
			},
			[]monitoringv1.Rule{
				{
					Alert: "AbsentNotContainersSwiftDispersionTaskExitCode",
					Expr:  intstr.FromString(`absent(swift_dispersion_task_exit_code)`),
					Labels: map[string]string{
						"context":       "absent-metrics",
						"severity":      "info",
						"support_group": "not-containers",
						"tier":          "os",
						"service":       "swift",
					},
				},
				{
					Alert: "AbsentNotContainersSwiftReconTaskExitCode",
					Expr:  intstr.FromString(`absent(swift_recon_task_exit_code)`),
					Labels: map[string]string{
						"context":       "absent-metrics",
						"severity":      "info",
						"support_group": "not-containers",
						"tier":          "os",
						"service":       "swift",
					},
				},
			},
		),
		Entry("complex expression test case 1",
			monitoringv1.Rule{
				Alert: "OpenstackSwiftUsedSpace",
				Expr:  intstr.FromString(`max(predict_linear(global:swift_cluster_storage_used_percent_average[1w], 60 * 60 * 24 * 30)) > 0.8`),
				Labels: map[string]string{
					"support_group": "not-containers",
					"tier":          "os",
					"service":       "swift",
				},
			},
			[]monitoringv1.Rule{{
				Alert: "AbsentNotContainersSwiftGlobalSwiftClusterStorageUsedPercentAverage",
				Expr:  intstr.FromString(`absent(global:swift_cluster_storage_used_percent_average)`),
				Labels: map[string]string{
					"context":       "absent-metrics",
					"severity":      "info",
					"support_group": "not-containers",
					"tier":          "os",
					"service":       "swift",
				},
			}},
		),
		Entry("complex expression test case 2",
			monitoringv1.Rule{
				Alert: "OpenstackLimesHttpErrors",
				Expr:  intstr.FromString(`sum(increase(http_requests_total{kubernetes_namespace="limes",code=~"5.*"}[1h])) by (kubernetes_name) > 0`),
				Labels: map[string]string{
					"support_group": "containers",
					"service":       "limes",
				},
			},
			[]monitoringv1.Rule{{
				Alert: "AbsentContainersLimesHttpRequestsTotal",
				Expr:  intstr.FromString(`absent(http_requests_total)`),
				Labels: map[string]string{
					"context":       "absent-metrics",
					"severity":      "info",
					"support_group": "containers",
					"service":       "limes",
				},
			}},
		),
		Entry("alert rule that uses label matching against the internal '__name__' label in expression",
			monitoringv1.Rule{
				Alert: "OpenstackLimesSuspendedScrapes",
				Expr:  intstr.FromString(`sum(increase({__name__=~'limes_suspended_scrapes'}[15m])) BY (os_cluster, service, service_name) > 0`),
				Labels: map[string]string{
					"support_group": "containers",
					"service":       "limes",
				},
			},
			[]monitoringv1.Rule{{
				Alert: "AbsentContainersLimesSuspendedScrapes",
				Expr:  intstr.FromString(`absent(limes_suspended_scrapes)`),
				Labels: map[string]string{
					"context":       "absent-metrics",
					"severity":      "info",
					"support_group": "containers",
					"service":       "limes",
				},
			}},
		),
		Entry("alert rule that already uses 'absent' function for the metric used in the expression",
			monitoringv1.Rule{
				Alert: "OpenstackLimesFailedScrapes",
				Expr:  intstr.FromString(`absent(limes_failed_scrapes) or sum(increase(limes_failed_scrapes[5m])) BY (os_cluster, service, service_name) > 0`),
				Labels: map[string]string{
					"support_group": "containers",
					"service":       `{{ $labels.service_name }}`,
				},
			},
			nil, // no absence alert rules should be generated for this alert
		),
		Entry("alert rule that uses 'absent' function but for a different metric in the expression",
			monitoringv1.Rule{
				Alert: "OpenstackLimesUnexpectedServiceRoleAssignments",
				Expr:  intstr.FromString(`absent(openstack_assignments_per_service{service_name="service"}) or max(openstack_assignments_per_role{role_name="resource_service"}) > 1`),
				Labels: map[string]string{
					"support_group": "containers",
					"service":       "limes",
				},
			},
			[]monitoringv1.Rule{{
				Alert: "AbsentContainersLimesOpenstackAssignmentsPerRole",
				Expr:  intstr.FromString(`absent(openstack_assignments_per_role)`),
				Labels: map[string]string{
					"context":       "absent-metrics",
					"severity":      "info",
					"support_group": "containers",
					"service":       "limes",
				},
			}},
		),
		Entry("alert rule with 'no_alert_on_absence' label",
			monitoringv1.Rule{
				Alert: "OpenstackSwiftMismatchedRings",
				Expr:  intstr.FromString(`(swift_cluster_md5_not_matched{kind="ring"} - swift_cluster_md5_errors{kind="ring"}) > 0`),
				Labels: map[string]string{
					"support_group":       "not-containers",
					"tier":                "os",
					"service":             "swift",
					"no_alert_on_absence": "true",
				},
			},
			nil, // absence alerts are not generated for record rules
		),
		Entry("record rule",
			monitoringv1.Rule{
				Record: "predict_linear_global_cluster_storage_used_percent_average",
				Expr:   intstr.FromString(`max(predict_linear(global:swift_cluster_storage_used_percent_average[1w], 60 * 60 * 24 * 30)) > 0.8`),
				Labels: map[string]string{
					"support_group": "not-containers",
					"tier":          "os",
					"service":       "swift",
				},
			},
			nil, // absence alerts are not generated for record rules
		),
	)

	Describe("Parsing alert rule expressions with AbsentLabel", func() {
		absentLabel := AbsentLabel{
			"namespace": true,
			"pod":       true,
		}
		baseLabels := map[string]string{
			"context":       "absent-metrics",
			"severity":      "info",
			"support_group": "containers",
			"service":       "k8s",
		}
		ruleLabels := map[string]string{
			"support_group": "containers",
			"service":       "k8s",
		}

		// checkRules is a helper that verifies alert names and expressions in order.
		checkRules := func(actual []monitoringv1.Rule, wantAlerts []string, wantExprs []string) {
			GinkgoHelper()
			Expect(actual).To(HaveLen(len(wantAlerts)))
			for i := range wantAlerts {
				Expect(actual[i].Alert).To(Equal(wantAlerts[i]))
				Expect(actual[i].Expr.String()).To(Equal(wantExprs[i]))
				Expect(actual[i].Labels).To(Equal(baseLabels))
			}
		}

		It("nil absentLabel: generates only bare absent() rule — identical to pre-feature behaviour", func() {
			rule := monitoringv1.Rule{
				Alert:  "SomePodCrashing",
				Expr:   intstr.FromString(`kube_pod_status_phase{namespace="production",pod="api-server",phase="Failed"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(actual).To(HaveLen(1))
			Expect(actual[0].Expr.String()).To(Equal(`absent(kube_pod_status_phase)`))
		})

		It("empty absentLabel: generates only bare absent() rule — identical to pre-feature behaviour", func() {
			rule := monitoringv1.Rule{
				Alert:  "SomePodCrashing",
				Expr:   intstr.FromString(`kube_pod_status_phase{namespace="production",pod="api-server",phase="Failed"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, AbsentLabel{})
			Expect(err).ToNot(HaveOccurred())
			Expect(actual).To(HaveLen(1))
			Expect(actual[0].Expr.String()).To(Equal(`absent(kube_pod_status_phase)`))
		})

		It("single occurrence with both requested labels: bare rule + one labeled rule", func() {
			rule := monitoringv1.Rule{
				Alert:  "SomePodCrashing",
				Expr:   intstr.FromString(`kube_pod_status_phase{namespace="production",pod="api-server",phase="Failed"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, absentLabel)
			Expect(err).ToNot(HaveOccurred())
			checkRules(actual,
				// sorted alphabetically; suffix is key-sorted: namespace=production, pod=api-server
				[]string{
					"AbsentContainersK8sKubePodStatusPhase",
					"AbsentContainersK8sKubePodStatusPhaseProductionApiServer",
				},
				[]string{
					`absent(kube_pod_status_phase)`,
					`absent(kube_pod_status_phase{namespace="production",pod="api-server"})`,
				},
			)
		})

		It("single occurrence with only one of the requested labels: bare rule + one labeled rule", func() {
			rule := monitoringv1.Rule{
				Alert:  "SomeNamespaceMissing",
				Expr:   intstr.FromString(`my_metric{namespace="staging"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, absentLabel)
			Expect(err).ToNot(HaveOccurred())
			checkRules(actual,
				[]string{
					"AbsentContainersK8sMyMetric",
					"AbsentContainersK8sMyMetricStaging",
				},
				[]string{
					`absent(my_metric)`,
					`absent(my_metric{namespace="staging"})`,
				},
			)
		})

		It("single occurrence with no requested labels in selector: only bare rule", func() {
			rule := monitoringv1.Rule{
				Alert:  "SomeMetricMissing",
				Expr:   intstr.FromString(`my_metric{env="prod"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, absentLabel)
			Expect(err).ToNot(HaveOccurred())
			Expect(actual).To(HaveLen(1))
			Expect(actual[0].Expr.String()).To(Equal(`absent(my_metric)`))
		})

		It("metric appears twice with same labels: bare rule + one labeled rule (deduped)", func() {
			rule := monitoringv1.Rule{
				Alert:  "MetricUsedTwiceSameLabels",
				Expr:   intstr.FromString(`my_metric{namespace="prod"} > 70 and predict_linear(my_metric{namespace="prod"}[1h], 3600) > 100`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, absentLabel)
			Expect(err).ToNot(HaveOccurred())
			checkRules(actual,
				[]string{
					"AbsentContainersK8sMyMetric",
					"AbsentContainersK8sMyMetricProd",
				},
				[]string{
					`absent(my_metric)`,
					`absent(my_metric{namespace="prod"})`,
				},
			)
		})

		It("metric appears twice with different label values: bare rule + two labeled rules", func() {
			rule := monitoringv1.Rule{
				Alert:  "MetricTwoDifferentNamespaces",
				Expr:   intstr.FromString(`my_metric{namespace="prod"} > 0 or my_metric{namespace="staging"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, absentLabel)
			Expect(err).ToNot(HaveOccurred())
			// bare + prod + staging — sorted alphabetically by alert name
			checkRules(actual,
				[]string{
					"AbsentContainersK8sMyMetric",
					"AbsentContainersK8sMyMetricProd",
					"AbsentContainersK8sMyMetricStaging",
				},
				[]string{
					`absent(my_metric)`,
					`absent(my_metric{namespace="prod"})`,
					`absent(my_metric{namespace="staging"})`,
				},
			)
		})

		It("metric appears twice: one occurrence has no requested label → one combo is empty → only bare + non-empty combo", func() {
			rule := monitoringv1.Rule{
				Alert:  "MetricOneWithOneMissingLabel",
				Expr:   intstr.FromString(`my_metric{namespace="prod"} > 0 or my_metric{env="other"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, absentLabel)
			Expect(err).ToNot(HaveOccurred())
			// bare rule + one labeled rule for the occurrence that had namespace
			checkRules(actual,
				[]string{
					"AbsentContainersK8sMyMetric",
					"AbsentContainersK8sMyMetricProd",
				},
				[]string{
					`absent(my_metric)`,
					`absent(my_metric{namespace="prod"})`,
				},
			)
		})

		It("non-equality (regex) matcher for a requested label: label not collected → only bare rule", func() {
			rule := monitoringv1.Rule{
				Alert:  "RegexLabelMatcher",
				Expr:   intstr.FromString(`my_metric{namespace=~"prod.*"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, absentLabel)
			Expect(err).ToNot(HaveOccurred())
			Expect(actual).To(HaveLen(1))
			Expect(actual[0].Expr.String()).To(Equal(`absent(my_metric)`))
		})

		It(`wildcard "*" matches every non-__name__ label: collects all equality matchers`, func() {
			rule := monitoringv1.Rule{
				Alert:  "AllLabelsCollected",
				Expr:   intstr.FromString(`my_metric{namespace="prod",pod="api",env="staging"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, AbsentLabel{"*": true})
			Expect(err).ToNot(HaveOccurred())
			// Suffix is built from sorted keys: env, namespace, pod → "Staging_Prod_Api".
			checkRules(actual,
				[]string{
					"AbsentContainersK8sMyMetric",
					"AbsentContainersK8sMyMetricStagingProdApi",
				},
				[]string{
					`absent(my_metric)`,
					`absent(my_metric{env="staging",namespace="prod",pod="api"})`,
				},
			)
		})

		It(`prefix wildcard "label_*" matches only labels starting with the prefix`, func() {
			rule := monitoringv1.Rule{
				Alert:  "PrefixWildcard",
				Expr:   intstr.FromString(`my_metric{label_a="x",label_b="y",other="z"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, AbsentLabel{"label_*": true})
			Expect(err).ToNot(HaveOccurred())
			// label_a, label_b collected; "other" excluded.
			checkRules(actual,
				[]string{
					"AbsentContainersK8sMyMetric",
					"AbsentContainersK8sMyMetricXY",
				},
				[]string{
					`absent(my_metric)`,
					`absent(my_metric{label_a="x",label_b="y"})`,
				},
			)
		})

		It(`suffix wildcard "*_id" matches only labels ending with the suffix`, func() {
			rule := monitoringv1.Rule{
				Alert:  "SuffixWildcard",
				Expr:   intstr.FromString(`my_metric{request_id="r1",trace_id="t1",namespace="prod"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, AbsentLabel{"*_id": true})
			Expect(err).ToNot(HaveOccurred())
			checkRules(actual,
				[]string{
					"AbsentContainersK8sMyMetric",
					"AbsentContainersK8sMyMetricR1T1",
				},
				[]string{
					`absent(my_metric)`,
					`absent(my_metric{request_id="r1",trace_id="t1"})`,
				},
			)
		})

		It(`contains wildcard "*ace*" matches labels containing the substring`, func() {
			rule := monitoringv1.Rule{
				Alert:  "ContainsWildcard",
				Expr:   intstr.FromString(`my_metric{namespace="prod",trace_id="t1",pod="api"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, AbsentLabel{"*ace*": true})
			Expect(err).ToNot(HaveOccurred())
			// namespace and trace_id both contain "ace"; pod does not.
			checkRules(actual,
				[]string{
					"AbsentContainersK8sMyMetric",
					"AbsentContainersK8sMyMetricProdT1",
				},
				[]string{
					`absent(my_metric)`,
					`absent(my_metric{namespace="prod",trace_id="t1"})`,
				},
			)
		})

		It(`wildcard never matches the internal "__name__" label`, func() {
			// With "*" set, only real labels in the selector should be collected;
			// __name__ must not appear in the generated absent() expression.
			rule := monitoringv1.Rule{
				Alert:  "InternalNameSkipped",
				Expr:   intstr.FromString(`{__name__="my_metric",namespace="prod"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, AbsentLabel{"*": true})
			Expect(err).ToNot(HaveOccurred())
			checkRules(actual,
				[]string{
					"AbsentContainersK8sMyMetric",
					"AbsentContainersK8sMyMetricProd",
				},
				[]string{
					`absent(my_metric)`,
					`absent(my_metric{namespace="prod"})`,
				},
			)
		})

		It(`mixed exact + wildcard patterns: union of matches is collected`, func() {
			rule := monitoringv1.Rule{
				Alert:  "MixedPatterns",
				Expr:   intstr.FromString(`my_metric{namespace="prod",label_a="x",other="z"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, AbsentLabel{
				"namespace": true,
				"label_*":   true,
			})
			Expect(err).ToNot(HaveOccurred())
			checkRules(actual,
				[]string{
					"AbsentContainersK8sMyMetric",
					"AbsentContainersK8sMyMetricXProd",
				},
				[]string{
					`absent(my_metric)`,
					`absent(my_metric{label_a="x",namespace="prod"})`,
				},
			)
		})

		It(`pattern that matches no label in the selector: only bare rule`, func() {
			rule := monitoringv1.Rule{
				Alert:  "NoMatch",
				Expr:   intstr.FromString(`my_metric{env="prod"} > 0`),
				Labels: ruleLabels,
			}
			actual, err := parseRule(logger, rule, keepLabel, AbsentLabel{"label_*": true})
			Expect(err).ToNot(HaveOccurred())
			Expect(actual).To(HaveLen(1))
			Expect(actual[0].Expr.String()).To(Equal(`absent(my_metric)`))
		})
	})
})
