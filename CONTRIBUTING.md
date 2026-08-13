# Contributing

Contributions are welcome, especially live testing across different Omni, Xen Orchestra, and XCP-ng releases — in particular validating the golden-template build sequence described in [Compatibility and limitations](docs/compatibility.md#golden-template-build-needs-live-validation).

## Before opening a pull request

1. Open an issue for significant behavior changes.
2. Keep changes focused.
3. Run formatting, tests, and a local build.
4. Add or update documentation.
5. Describe any live Omni/Xen Orchestra testing completed.

```bash
gofmt -w $(find . -name '*.go' -not -path './vendor/*')
go mod tidy
go test ./...
go build ./cmd/omni-infra-provider-xoa
```

## Pull request information

Include:

- Problem being solved
- Design and tradeoffs
- Test coverage
- Omni version tested
- Xen Orchestra and XCP-ng version tested
- Talos version tested
- Upgrade or migration considerations

## Code expectations

- Preserve idempotent reconciliation.
- Do not log credentials, join tokens, or cloud-init secrets.
- Validate all provider input before creating resources.
- Prefer deterministic names and explicit ownership descriptions.
- Make deletion tolerant of already-absent resources.
- Keep Machine Class schema and Go structs synchronized.
- Add tests for image identity and validation logic.

## Generated code

Files under `api/specs` are generated protobuf output. Changes to the protobuf schema should include regenerated Go files and the generator/version information used — see [Development and releases](docs/development.md#regenerating-protobuf-code).

## License

By contributing, you agree that your contribution is licensed under the Mozilla Public License 2.0.
