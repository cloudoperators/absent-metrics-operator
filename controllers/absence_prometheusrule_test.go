// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
)

var _ = Describe("AbsencePrometheusRule", func() {
	pr := &monitoringv1.PrometheusRule{
		Name:      "foobar.alerts",
		Namespace: "outerspace",
		Labels: map[string]string{
			"prometheus":   "openstack",
			"thanos-ruler": "titan",
		},
	}

	DescribeTable("Name generation",
		func(tmplStr, expected string, shouldFail bool) {
			gen, err := CreateAbsencePromRuleNameGenerator(tmplStr)
			Expect(err).ToNot(HaveOccurred())
			actual, err := gen(pr)
			if shouldFail {
				Expect(err).To(HaveOccurred())
				return
			}
			Expect(err).ToNot(HaveOccurred())
			Expect(actual).To(Equal(expected))
		},
		Entry("name that uses the original name",
			`{{ .metadata.name }}`,
			"foobar.alerts"+absencePromRuleNameSuffix,
			false,
		),
		Entry("name that uses the namespace",
			`{{ .metadata.namespace }}`,
			"outerspace"+absencePromRuleNameSuffix,
			false,
		),
		Entry("name that uses the original name and namespace",
			`{{ .metadata.name }}-{{ .metadata.namespace }}`,
			"foobar.alerts-outerspace"+absencePromRuleNameSuffix,
			false,
		),
		Entry("name with prometheus label",
			`{{ .metadata.labels.prometheus }}`,
			"openstack"+absencePromRuleNameSuffix,
			false,
		),
		Entry("name with thanos-ruler label if it exists",
			DefaultAbsencePromRuleNameTemplate,
			"titan"+absencePromRuleNameSuffix,
			false,
		),
		Entry("name that references nonexistent metadata",
			`{{ .metadata.doesntexist }}`,
			"",
			true,
		),
	)
})
