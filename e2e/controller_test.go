// SPDX-FileCopyrightText: 2020 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/sapcc/absent-metrics-operator/controllers"
)

var _ = Describe("Controller", Ordered, func() {
	// Throughout the individual tests we will use two terms:
	//   - PromRule: this refers to a PrometheusRule resource which contains alert rules.
	//   - AbsencePromRule: this refers to a PrometheusRule resource created by the controller that
	//                      contains the corresponding absence alert rules for a PrometheusRule resource.
	//
	// The fixtures/start-data contains four PromRule(s) which, at this stage, should have
	// already been added to our test cluster.
	// They are called (namespace/name):
	// 	 - resmgmt/kubernetes-keppel.alerts
	// 	 - resmgmt/openstack-limes-api.alerts
	// 	 - resmgmt/openstack-limes-roleassign.alerts
	// 	 - swift/openstack-swift.alerts

	var (
		k8sAbsencePRName = "kubernetes-absent-metric-alert-rules"
		osAbsencePRName  = "openstack-absent-metric-alert-rules"

		swiftNs   = "swift"
		resmgmtNs = "resmgmt"

		resmgmtOSAbsencePromRule = getFixture("resmgmt_openstack_absent_metrics_alert_rules.yaml")
	)

	Describe("Create", func() {
		Context("during initial run", func() {
			It("should create "+k8sAbsencePRName+" in "+resmgmtNs+" namespace", func() {
				expectPromRulesToMatch(
					getFixture("resmgmt_kubernetes_absent_metric_alert_rules.yaml"),
					getPromRule(newObjKey(resmgmtNs, k8sAbsencePRName)),
				)
			})

			It("should create "+osAbsencePRName+" in "+resmgmtNs+" namespace", func() {
				expectPromRulesToMatch(
					resmgmtOSAbsencePromRule,
					getPromRule(newObjKey(resmgmtNs, osAbsencePRName)),
				)
			})

			It("should not create "+osAbsencePRName+" in "+swiftNs+" namespace", func() {
				expectToNotFindPromRule(newObjKey(swiftNs, osAbsencePRName))
			})
		})

		Context("after removing absent-metrics-operator/disable label", func() {
			It("should create "+osAbsencePRName+" in "+swiftNs+" namespace", func() {
				// Get the concerning PromRule.
				pr := getPromRule(newObjKey(swiftNs, "openstack-swift.alerts"))

				// Update the "disable" label on the PromRule.
				pr.Labels["absent-metrics-operator/disable"] = "false"
				Expect(k8sClient.Update(ctx, &pr)).To(Succeed())

				// Check that the corresponding AbsencePromRule was created.
				waitForControllerToProcess()
				expectPromRulesToMatch(
					getFixture("swift_openstack_absent_metric_alert_rules.yaml"),
					getPromRule(newObjKey(swiftNs, osAbsencePRName)),
				)
			})
		})
	})

	Describe("Update", func() {
		objKey := newObjKey(swiftNs, "openstack-swift.alerts")
		prObjKey := newObjKey(swiftNs, osAbsencePRName)
		fooBar := "foo_bar"
		barFoo := "bar_foo"

		Context("by adding a new alert rule", func() {
			It("should update "+osAbsencePRName+" in "+swiftNs+" namespace", func() {
				// Get the concerning PromRule.
				pr := getPromRule(objKey)

				// Add a new alert rule.
				rule := createMockRule(fooBar)
				pr.Spec.Groups[0].Rules = append(pr.Spec.Groups[0].Rules, rule)
				Expect(k8sClient.Update(ctx, &pr)).To(Succeed())

				// Generate the corresponding absence alert rules.
				expected := checkErrAndReturnResult(controllers.ParseRuleGroups(logger, pr.Spec.Groups, pr.GetName(), keepLabel, nil))

				// Get the updated AbsencePromRule from the server and check if it has the
				// corresponding absence alert rule.
				waitForControllerToProcess()
				absencePR := getPromRule(prObjKey)
				actual := absencePR.Spec.Groups
				Expect(actual).To(Equal(expected))
			})
		})

		Context("by updating an existing alert rule", func() {
			It("should update "+osAbsencePRName+" in "+swiftNs+" namespace", func() {
				// Get the concerning PromRule.
				pr := getPromRule(objKey)

				// Update an existing alert rule. Replace alert that has foo_bar metric
				// with bar_foo.
			OuterLoop:
				for gIdx, g := range pr.Spec.Groups {
					for rIdx, r := range g.Rules {
						if strings.Contains(r.Expr.String(), fooBar) {
							pr.Spec.Groups[gIdx].Rules[rIdx] = createMockRule(barFoo)
							break OuterLoop
						}
					}
				}
				Expect(k8sClient.Update(ctx, &pr)).To(Succeed())

				// Generate the corresponding absence alert rules.
				expected := checkErrAndReturnResult(controllers.ParseRuleGroups(logger, pr.Spec.Groups, pr.GetName(), keepLabel, nil))

				// Get the updated AbsencePromRule from the server and check if the
				// corresponding absence alert rule has been updated.
				waitForControllerToProcess()
				absencePR := getPromRule(prObjKey)
				actual := absencePR.Spec.Groups
				Expect(actual).To(Equal(expected))
			})
		})
	})

	Describe("Cleanup", func() {
		Context("when a PrometheusRule is deleted", func() {
			It("should delete "+k8sAbsencePRName+" in "+resmgmtNs+" namespace", func() {
				deletePromRule(newObjKey(resmgmtNs, "kubernetes-keppel.alerts"))
				waitForControllerToProcess()
				expectToNotFindPromRule(newObjKey(resmgmtNs, k8sAbsencePRName))
			})

			It("should delete orphaned absence alert rules from "+osAbsencePRName+" in "+resmgmtNs+" namespace", func() {
				// AbsencePromRule 'openstack-absent-metric-alert-rules' in 'resmgmt'
				// namespace holds the aggregate absence alert rules for both
				// 'openstack-limes-api.alerts' and 'openstack-limes-roleassign.alerts'
				// PromRules.
				limesRolePRName := "openstack-limes-roleassign.alerts"
				deletePromRule(newObjKey(resmgmtNs, limesRolePRName))

				expected := make([]monitoringv1.RuleGroup, 0, len(resmgmtOSAbsencePromRule.Spec.Groups)-1)
				for _, g := range resmgmtOSAbsencePromRule.Spec.Groups {
					if !strings.Contains(g.Name, limesRolePRName) {
						expected = append(expected, g)
					}
				}

				// Deleting one PromRule should only result in cleanup of its
				// corresponding absence alert rules from AbsencePromRule instead of
				// deleting the AbsencePromRule itself.
				waitForControllerToProcess()
				actual := getPromRule(newObjKey(resmgmtNs, osAbsencePRName))
				Expect(actual.Spec.Groups).To(Equal(expected))
			})
		})

		Context("when a rule group is deleted from a PrometheusRule", func() {
			It("should delete the corresponding rule group from the AbsencePromRule "+osAbsencePRName+" in "+resmgmtNs+" namespace", func() {
				// We will remove one of the two rule groups.
				prName := "openstack-limes-api.alerts"
				pr := getPromRule(newObjKey(resmgmtNs, prName))
				ruleGroupName := pr.Spec.Groups[0].Name
				pr.Spec.Groups = pr.Spec.Groups[1:]
				Expect(k8sClient.Update(ctx, &pr)).To(Succeed())

				waitForControllerToProcess()
				actual := getPromRule(newObjKey(resmgmtNs, osAbsencePRName))
				groups := make([]string, 0, len(actual.Spec.Groups))
				for _, v := range actual.Spec.Groups {
					groups = append(groups, v.Name)
				}
				Expect(groups).ToNot(ContainElement(controllers.AbsenceRuleGroupName(prName, ruleGroupName)))
			})
		})

		Context("when a PrometheusRule has no alert rules", func() {
			It("should delete "+osAbsencePRName+" in "+resmgmtNs+" namespace", func() {
				// We will remove all alert rules from a PromRule which should result in
				// the deletion of its corresponding AbsencePromRule since no absence alert
				// rules will be generated and the resulting AbsencePromRule would be
				// empty.
				pr := getPromRule(newObjKey(resmgmtNs, "openstack-limes-api.alerts"))
				pr.Spec.Groups = []monitoringv1.RuleGroup{}
				Expect(k8sClient.Update(ctx, &pr)).To(Succeed())

				waitForControllerToProcess()
				expectToNotFindPromRule(newObjKey(resmgmtNs, osAbsencePRName))
			})
		})
	})

	Describe("Disabling", func() {
		objKey := newObjKey(swiftNs, "openstack-swift.alerts")
		prObjKey := newObjKey(swiftNs, osAbsencePRName)

		Context("for a specific alert rule", func() {
			It("should delete the corresponding absence alert rule from "+osAbsencePRName+" in "+swiftNs+" namespace", func() {
				// Add the 'no_alert_on_absence' label to the first rule of first group in
				// the PromRule. This should result in the deletion of the corresponding
				// absence alert rule from the AbsencePromRule.
				pr := getPromRule(objKey)
				pr.Spec.Groups[0].Rules[0].Labels["no_alert_on_absence"] = "true"
				Expect(k8sClient.Update(ctx, &pr)).To(Succeed())

				// Generate the corresponding absence alert rules.
				expected := checkErrAndReturnResult(controllers.ParseRuleGroups(logger, pr.Spec.Groups, pr.GetName(), keepLabel, nil))

				// Check that the corresponding absence alert rule was removed.
				waitForControllerToProcess()
				aPR := getPromRule(prObjKey)
				Expect(aPR.Spec.Groups).To(Equal(expected))
			})
		})

		Context("for an entire PrometheusRule", func() {
			It("should delete "+osAbsencePRName+" in "+swiftNs+" namespace", func() {
				// Add the 'absent-metrics-operator/disable' label to the PromRule. This
				// should result in the deletion of the corresponding AbsencePromRule.
				pr := getPromRule(objKey)
				pr.Labels["absent-metrics-operator/disable"] = "true"
				Expect(k8sClient.Update(ctx, &pr)).To(Succeed())

				waitForControllerToProcess()
				expectToNotFindPromRule(prObjKey)
			})
		})
	})

	Describe("Orphaned gauge metrics", func() {
		It("should be cleaned up", func() {
			method := http.MethodGet
			path := "/metrics"
			var requestBody io.Reader
			request := httptest.NewRequest(method, path, requestBody)

			recorder := httptest.NewRecorder()
			handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
			handler.ServeHTTP(recorder, request)
			response := recorder.Result()
			Expect(response.StatusCode).To(Equal(http.StatusOK))

			// Write actual content to file to make it easy to copy the computed result over to the
			// fixture path when a new test is added or an existing one is modified.
			fixturePath := checkErrAndReturnResult(filepath.Abs("fixtures/metrics.prom"))
			actualBytes := checkErrAndReturnResult(io.ReadAll(response.Body))
			actualPath := fixturePath + ".actual"
			Expect(os.WriteFile(actualPath, actualBytes, 0o600)).To(Succeed())

			cmd := exec.Command("diff", "-u", fixturePath, actualPath)
			cmd.Stdin = nil
			cmd.Stdout = GinkgoWriter
			cmd.Stderr = os.Stderr
			Expect(cmd.Run()).To(Succeed(), "%s %s: body does not match", method, path)
		})
	})
})

// /////////////////////////////////////////////////////////////////////////////
// Helper functions

func checkErrAndReturnResult[T any](result T, err error) T {
	GinkgoHelper()
	Expect(err).ToNot(HaveOccurred())
	return result
}

func expectPromRulesToMatch(expected, actual monitoringv1.PrometheusRule) {
	GinkgoHelper()
	Expect(actual.Labels).To(Equal(expected.Labels))
	Expect(actual.Annotations).To(Equal(expected.Annotations))
	Expect(actual.Spec).To(Equal(expected.Spec))
}

// Wait for controller to resync and complete its processing.
var waitForControllerToProcess = func() { time.Sleep(500 * time.Millisecond) }

func newObjKey(namespace, name string) client.ObjectKey {
	return client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}
}

func createMockRule(metric string) monitoringv1.Rule {
	duration := monitoringv1.Duration("5m")
	return monitoringv1.Rule{
		Alert: cases.Title(language.English).String(metric),
		Expr:  intstr.FromString(metric + " > 0"),
		For:   &duration,
		Labels: map[string]string{
			"tier":          "tier",
			"service":       "service",
			"support_group": "support_group",
		},
	}
}

func getFixture(name string) monitoringv1.PrometheusRule {
	GinkgoHelper()
	b, err := os.ReadFile(filepath.Join("fixtures", name))
	Expect(err).ToNot(HaveOccurred())
	var pr monitoringv1.PrometheusRule
	Expect(yaml.Unmarshal(b, &pr)).To(Succeed())
	return pr
}

func getPromRule(key client.ObjectKey) monitoringv1.PrometheusRule {
	GinkgoHelper()
	var pr monitoringv1.PrometheusRule
	Expect(k8sClient.Get(ctx, key, &pr)).To(Succeed())
	return pr
}

func expectToNotFindPromRule(key client.ObjectKey) {
	GinkgoHelper()
	var pr monitoringv1.PrometheusRule
	err := k8sClient.Get(ctx, key, &pr)
	Expect(err).To(HaveOccurred())
	Expect(apierrors.IsNotFound(err)).To(BeTrue())
}

func deletePromRule(key client.ObjectKey) {
	GinkgoHelper()
	var pr monitoringv1.PrometheusRule
	pr.SetName(key.Name)
	pr.SetNamespace(key.Namespace)
	Expect(k8sClient.Delete(ctx, &pr)).To(Succeed())
}
