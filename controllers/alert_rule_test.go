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

		DescribeTable("--absent-labels behaviour",
			func(in monitoringv1.Rule, expectedExpr string) {
				actual, err := parseRule(logger, in, keepLabel, absentLabel)
				Expect(err).ToNot(HaveOccurred())
				Expect(actual).To(HaveLen(1))
				Expect(actual[0].Expr.String()).To(Equal(expectedExpr))
			},

			Entry("single occurrence: includes requested labels present in selector",
				monitoringv1.Rule{
					Alert: "SomePodCrashing",
					Expr:  intstr.FromString(`kube_pod_status_phase{namespace="production",pod="api-server",phase="Failed"} > 0`),
					Labels: map[string]string{
						"support_group": "containers",
						"service":       "k8s",
					},
				},
				// namespace and pod are both present with equality matchers → included
				`absent(kube_pod_status_phase{namespace="production",pod="api-server"})`,
			),

			Entry("requested label absent from selector → falls back to bare absent()",
				monitoringv1.Rule{
					Alert: "SomeMetricMissing",
					Expr:  intstr.FromString(`my_metric{env="prod"} > 0`),
					Labels: map[string]string{
						"support_group": "containers",
						"service":       "myapp",
					},
				},
				// neither namespace nor pod appear → bare absent()
				`absent(my_metric)`,
			),

			Entry("only one of the requested labels is present → includes only that one",
				monitoringv1.Rule{
					Alert: "SomeNamespaceMetricMissing",
					Expr:  intstr.FromString(`my_metric{namespace="staging"} > 0`),
					Labels: map[string]string{
						"support_group": "containers",
						"service":       "myapp",
					},
				},
				// namespace present, pod absent → only namespace included
				`absent(my_metric{namespace="staging"})`,
			),

			Entry("metric appears multiple times with same label values → consistent → includes label",
				monitoringv1.Rule{
					Alert: "MetricUsedTwiceSameLabels",
					Expr:  intstr.FromString(`my_metric{namespace="prod"} > 70 and predict_linear(my_metric{namespace="prod"}[1h], 3600) > 100`),
					Labels: map[string]string{
						"support_group": "containers",
						"service":       "myapp",
					},
				},
				// both occurrences have namespace="prod" → consistent → included
				`absent(my_metric{namespace="prod"})`,
			),

			Entry("metric appears multiple times with differing label values → inconsistent → falls back to bare absent()",
				monitoringv1.Rule{
					Alert: "MetricUsedTwiceDifferentLabels",
					Expr:  intstr.FromString(`my_metric{namespace="prod"} > 0 or my_metric{namespace="staging"} > 0`),
					Labels: map[string]string{
						"support_group": "containers",
						"service":       "myapp",
					},
				},
				// namespace="prod" vs namespace="staging" → inconsistent → bare absent()
				`absent(my_metric)`,
			),

			Entry("metric appears twice: one occurrence missing the label → inconsistent → bare absent()",
				monitoringv1.Rule{
					Alert: "MetricOneWithOneMissing",
					Expr:  intstr.FromString(`my_metric{namespace="prod"} > 0 or my_metric{env="prod"} > 0`),
					Labels: map[string]string{
						"support_group": "containers",
						"service":       "myapp",
					},
				},
				// second occurrence has no namespace matcher → inconsistent → bare absent()
				`absent(my_metric)`,
			),

			Entry("non-equality matcher (regex) for a requested label → not collected → bare absent()",
				monitoringv1.Rule{
					Alert: "RegexLabelMatcher",
					Expr:  intstr.FromString(`my_metric{namespace=~"prod.*"} > 0`),
					Labels: map[string]string{
						"support_group": "containers",
						"service":       "myapp",
					},
				},
				// namespace uses regex → not an equality matcher → not collected → bare absent()
				`absent(my_metric)`,
			),

			Entry("nil absentLabel behaves identically to existing behaviour",
				monitoringv1.Rule{
					Alert: "NilAbsentLabel",
					Expr:  intstr.FromString(`my_metric{namespace="prod"} > 0`),
					Labels: map[string]string{
						"support_group": "containers",
						"service":       "myapp",
					},
				},
				// nil passed explicitly by calling with absentLabel=nil below
				// (this entry uses the outer absentLabel but we test bare absent() separately below)
				`absent(my_metric{namespace="prod"})`,
			),
		)

		It("generates bare absent() when absentLabel is nil, even if selector has matching labels", func() {
			rule := monitoringv1.Rule{
				Alert: "NilAbsentLabel",
				Expr:  intstr.FromString(`my_metric{namespace="prod"} > 0`),
				Labels: map[string]string{
					"support_group": "containers",
					"service":       "myapp",
				},
			}
			actual, err := parseRule(logger, rule, keepLabel, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(actual).To(HaveLen(1))
			Expect(actual[0].Expr.String()).To(Equal(`absent(my_metric)`))
		})

		It("generates bare absent() when absentLabel is empty map, even if selector has matching labels", func() {
			rule := monitoringv1.Rule{
				Alert: "EmptyAbsentLabel",
				Expr:  intstr.FromString(`my_metric{namespace="prod"} > 0`),
				Labels: map[string]string{
					"support_group": "containers",
					"service":       "myapp",
				},
			}
			actual, err := parseRule(logger, rule, keepLabel, AbsentLabel{})
			Expect(err).ToNot(HaveOccurred())
			Expect(actual).To(HaveLen(1))
			Expect(actual[0].Expr.String()).To(Equal(`absent(my_metric)`))
		})
	})
})
