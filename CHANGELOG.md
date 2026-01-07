<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Changelog

All notable changes to the absent metrics operator will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

<!---
The changes should be grouped using the following categories (in order of precedence):
- Added: for new features
- Changed: for changes in existing functionality
- Fixed: for any bug fixes
- Removed: for now removed features
- Deprecated: for soon-to-be removed features
-->

## [Unreleased]()

## 0.9.7 - 2025-12-03

### Changed

- Migrate github org from sapcc to cloudoperators

## 0.9.5 - 2023-10-06

### Changed

- Updated all dependencies to their latest version.

## 0.9.4 - 2023-05-08

### Changed

- Add [automaxprocs](https://github.com/uber-go/automaxprocs)
- Updated all dependencies and golang to their latest version.
- Renamed `doc` folder to `docs`.

## 0.9.3 - 2023-04-04

### Changed

- Update Golang to 1.20.
- Updated all dependencies to their latest version.
- Use unprivileged `appuser` with UID/GID 4200 instead of nobody user.
- Use `sigs.k8s.io/yaml` instead of `github.com/ghodss/yaml`.

## 0.9.2 - 2022-11-08

### Changed

- Updated all dependencies to their latest version.
- Update resources using `Patch` instead of `Update`.
- Use RFC3339 time format for logs.

### Fixed

- Update AbsencePrometheusRule if only labels have changed.
- Do not requeue resources that do not exist.

## 0.9.1 - 2022-11-07

### Changed

- Do not log error if corresponding AbsencePrometheusRule can not be retrieved or does not
  exist during clean up.
- Use debug log level for less important log messages.

### Fixed

- `debug` flag.

## 0.9.0 - 2022-11-02

### Added

- Carry over `support_group` labels from original alert rules.

### Changed

- Updated all dependencies to their latest version.

### Fixed

- Add missing Kubebuilder annotations.
- Logging during metric expression parsing.
- `keep-labels` flag parsing.
- Prevent getting stuck during clean up of orphaned absence alert rules when the
  corresponding AbsencePrometheusRule doesn't exist.
- Skip metrics that match against the internal `__name__` label and use `absent` function.

### Deprecated

- Support for determining `tier` label [heuristically](./docs/playbook.md) has been
  deprecated and will be removed in a later version.

## 0.8.0 - 2022-04-12

### Added

- Use Kubebuilder for scaffolding.
- `absent-metrics-operator/updated-at` annotation to operator generated `PrometheusRule`
  resources which specifies the time (UTC) at which this resource was updated by the
  operator.

### Changed

- Update Ginkgo testing framework to v2.
- Updated all dependencies to their latest version.

## 0.7.3 - 2021-11-29

### Changed

- Updated all dependencies to their latest version.

## 0.7.2 - 2021-09-29

### Changed

- Updated Go to `1.17` and all dependencies to their latest version.

## 0.7.1 - 2020-11-17

### Fixed

- Clean up of PrometheusRule resources for which the operator is disabled.

## 0.7.0 - 2020-09-28

### Fixed

- Clean up orphaned gauge metrics.

## 0.6.0 - 2020-08-26

### Added

- Manual maintenance task.

### Fixed

- Delete timeseries concerning `PrometheusRules` that no longer exist.

## 0.5.2 - 2020-08-21

### Fixed

- A bug that was introduced in the previous release.

## 0.5.1 - 2020-08-21

### Changed

- Prevent superfluous processing if the resource doesn't have any alert rules.

## 0.5.0 - 2020-08-21

### Added

- `context` label to absence alerts.

## 0.4.0 - 2020-08-20

### Removed

- `playbook` label from absence alerts.

## 0.3.0 - 2020-08-20

### Added

- Parse vector selectors that use label matching against the internal
  `__name__` label.

## 0.2.0 - 2020-08-18

### Added

- Operator can be disabled for a specific alert rule.
- `playbook` label to absence alerts.
- `keep-labels` flag for specifying which labels to carry over from alert
  rules.

## 0.1.0 - 2020-08-13

### Added

- Initial release.
