# Support

This is a community project. It is not covered by official Sidero Labs or Vates support unless those vendors explicitly state otherwise.

## Before requesting help

Review:

- [Installation](docs/installation.md)
- [Configuration reference](docs/configuration.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Compatibility and limitations](docs/compatibility.md)

## Bug reports

Include:

- Provider release tag or commit (run `omni-infra-provider-xoa --version`, or read the `version` field in the startup log line)
- Omni version
- Xen Orchestra and XCP-ng version
- Talos version
- Deployment method: Docker Compose or Kubernetes
- Sanitized Machine Class provider data
- Sanitized provider logs
- Expected and actual behavior
- Whether the issue occurs with automatic images or `template_id`

Do not post credentials, join tokens, private keys, or complete cloud-init data.

## Feature requests

Describe the operational problem, not only the proposed implementation. Useful requests include:

- Additional architectures
- Image cache garbage collection
- Private Image Factory authentication
- More Xen Orchestra VM options (e.g. a per-Machine-Class `uefi` toggle)
- Migration to the Xen Orchestra REST API (v2 SDK) once it matures
- Metrics and health endpoints
- Existing-VM reconciliation

## Vendor product issues

Use the appropriate vendor support channel when the problem reproduces independently of this provider:

- Omni and Talos documentation: <https://docs.siderolabs.com/>
- Xen Orchestra and XCP-ng documentation: <https://docs.xen-orchestra.com/>, <https://docs.xcp-ng.org/>
