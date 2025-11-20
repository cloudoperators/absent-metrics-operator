<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Absent Metrics Operator

[![GitHub Release](https://img.shields.io/github/v/release/sapcc/absent-metrics-operator)](https://github.com/sapcc/absent-metrics-operator/releases/latest)
[![CI](https://github.com/sapcc/absent-metrics-operator/actions/workflows/ci.yaml/badge.svg)](https://github.com/sapcc/absent-metrics-operator/actions/workflows/ci.yaml)
[![Coverage Status](https://coveralls.io/repos/github/sapcc/absent-metrics-operator/badge.svg?branch=master)](https://coveralls.io/github/sapcc/absent-metrics-operator?branch=master)
[![Go Report Card](https://goreportcard.com/badge/github.com/sapcc/absent-metrics-operator)](https://goreportcard.com/report/github.com/sapcc/absent-metrics-operator)

In this document:

- [Terminology](#terminology)
- [Overview](#overview)
- [Motivation](#motivation)
- [Installation](#installation)
- [Usage](#usage)
  - [Metrics](#metrics)

In other documents:

- [Absence alert rule definition](./docs/absence-alert-rule-definition.md)
- [Playbook for operators](./docs/playbook.md)

## Terminology

An **_absence alert rule_** is an alert rule that alerts on the absence of a metric.

A `PrometheusRule` is a custom resource defined by the [Prometheus
operator](prometheus-operator), it is used to define a set of alerting rules. Within the
absent metrics operator documentation and source code, an **_AbsencePrometheusRule_** is a
`PrometheusRule` resource created (and managed) by the absent metrics operator that
defines corresponding **_absence alert rules_** for the metrics that were used in the
alert rule definitions in a `PrometheusRule`.

## Overview

The absent metrics operator is a companion operator for the [Prometheus
Operator][prometheus-operator].

It monitors all the `PrometheusRule` resources deployed across a
Kubernetes cluster and creates corresponding _absence alert rules_ for
the alert rules defined in those resources.

## Motivation

Consider the following alert rule definition:

```yaml
alert: ImportantAlert
expr: foo_bar > 0
for: 5m
labels:
  support_group: network
  service: foo
  severity: critical
annotations:
  summary: Data center is on fire!
```

This alert would never trigger if the metric `foo_bar` does not exist in
Prometheus.

This can be avoided by using the `absent()` function with the `or` operator so
the alert rule expression becomes:

```
absent(foo_bar) or foo_bar > 0
```

However, this gets tedious if you have hundreds of alerts deployed across the cluster.
There is also the element of human error, e.g. typo or forgetting to include
the `absent` function in the alert expression.

This problem is resolved by the absent metrics operator as it automatically creates the
corresponding alert rules that check and alert on metric absence.

For example, considering the alert rule mentioned above, the operator would generate the following _absence alert rule_:

```yaml
alert: AbsentNetworkFooBar
expr: absent(foo_bar)
for: 10m
labels:
  context: absent-metrics
  severity: info
  support_group: network
  service: foo
annotations:
  summary: missing foo_bar
  description: The metric 'foo_bar' is missing. 'ImportantAlert' alert using it may not fire as intended.
```

Refer to the _absence alert rule_ [definition
documentation](./docs/absence-alert-rule-definition.md) for more information on how these
alerts are generated and defined.

## Installation

You can build with `make`, install with `make install`, or `docker build`.

The `make install` target understands the conventional environment variables for choosing
install locations: `DESTDIR` and `PREFIX`.

## Usage

For usage instructions:

```
absent-metrics-operator --help
```

In case of a false positive, the operator can be disabled for a specific alert rule or the
entire `PrometheusRule` resource. Refer to the [playbook for operators](./docs/playbook.md#disable-the-operator)
for instructions.

### Metrics

Metrics are exposed at port `9659`. This port has been
[allocated](https://github.com/prometheus/prometheus/wiki/Default-port-allocations)
for the operator.

| Metric                                              | Labels                                            |
| --------------------------------------------------- | ------------------------------------------------- |
| `absent_metrics_operator_successful_reconcile_time` | `prometheusrule_namespace`, `prometheusrule_name` |

[prometheus-operator]: https://github.com/prometheus-operator/prometheus-operator

## Contributing

This project is open to feature requests/suggestions, bug reports etc. via [GitHub issues](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/creating-an-issue). Contribution and feedback are encouraged and always welcome. For more information about how to contribute, the project structure, as well as additional contribution information, see our [Contribution Guidelines](https://github.com/cloudoperators/absent-metrics-operator/blob/master/CONTRIBUTING.md).

## Code of Conduct

We as members, contributors, and leaders pledge to make participation in our community a harassment-free experience for everyone. By participating in this project, you agree to abide by its [Code of Conduct](https://github.com/SAP/.github/blob/main/CODE_OF_CONDUCT.md) at all times.

## Licensing

Copyright 2025 SAP SE or an SAP affiliate company and absent-metrics-operator contributors. Please see our [LICENSE](LICENSE) for copyright and license information. Detailed information including third-party components and their licensing/copyright information is available [via the REUSE tool](https://api.reuse.software/info/github.com/cloudoperators/absent-metrics-operator).