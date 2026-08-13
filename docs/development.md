# Development and releases

## Local prerequisites

- Go version declared in `go.mod`
- Docker with BuildKit
- Access to an Omni test instance
- Access to a disposable Xen Orchestra / XCP-ng environment

## Build and test

```bash
go mod tidy
go mod verify
go test ./...
go build -o _out/omni-infra-provider-xoa \
  ./cmd/omni-infra-provider-xoa
```

Docker:

```bash
docker build --pull --no-cache \
  -t omni-infra-provider-xoa:dev \
  .
```

Run help output:

```bash
docker run --rm omni-infra-provider-xoa:dev --help
```

## Source layout

```text
cmd/omni-infra-provider-xoa/        process startup and flags
internal/pkg/provider/provision.go  machine lifecycle reconciliation
internal/pkg/provider/image.go      Image Factory URL and golden-template handling
internal/pkg/provider/data/         Machine Class data and JSON schema
internal/pkg/provider/resources/    Omni/COSI provider state resource
api/specs/                          generated protobuf machine state
deploy/                             deployment examples
docs/                               operator and contributor documentation
```

## Changing Machine Class fields

Update both:

- `internal/pkg/provider/data/data.go`
- `internal/pkg/provider/data/schema.json`

Keep field names, types, defaults, and validation synchronized. Add tests to `helpers_test.go` or a new focused test file.

## Changing image behavior

Image identity and golden-template handling live in `internal/pkg/provider/image.go`. Preserve these properties:

- Deterministic name for the same asset URL
- Different names for different schematics or Talos versions
- Safe behavior during concurrent first-time requests within a single provider process
- Manual `template_id` override

## Icon

`cmd/omni-infra-provider-xoa/data/icon.svg` is a neutral placeholder, not the Xen Orchestra or XCP-ng logo (those are trademarked and weren't carried over from the source project, which used a VergeOS-branded mark). Swap it for your own icon if you want provider-specific branding in the Omni UI; it's embedded at build time via `//go:embed`.

## Regenerating protobuf code

`api/specs/specs.pb.go` and `api/specs/specs_vtproto.pb.go` are generated. After editing `api/specs/specs.proto`:

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go install github.com/planetscale/vtprotobuf/cmd/protoc-gen-go-vtproto@v0.6.1-0.20250313105119-ba97887b0a25

protoc -I api \
  --go_out=. --go_opt=module=github.com/kreove/omni-infra-provider-xoa \
  --go-vtproto_out=. --go-vtproto_opt=module=github.com/kreove/omni-infra-provider-xoa \
  --go-vtproto_opt=features=marshal+unmarshal+size+equal+clone \
  api/specs/specs.proto
```

Use the exact plugin versions declared in `go.mod` when regenerating for a release.

## Live test checklist

The golden-template build sequence, VM clone with cloud-init, power-on, and full deprovisioning have already been validated once against a live XCP-ng pool — see [Compatibility and limitations](compatibility.md#findings-from-live-validation) for what that covered and what it found. `internal/pkg/provider/live_test.go` contains that validation as a repeatable, opt-in integration test: it's skipped by default and only runs when `XOA_LIVE_ENDPOINT` is set, but exercises the real `importGoldenTemplate`/`createVM`/`Deprovision` code paths (not a reimplementation) against a real pool. Run it against your own environment before a release:

```bash
XOA_LIVE_ENDPOINT=wss://xoa.example.com \
XOA_LIVE_USERNAME=... XOA_LIVE_PASSWORD=... \
XOA_LIVE_POOL_ID=... XOA_LIVE_SR_ID=... XOA_LIVE_NETWORK_ID=... \
go test ./internal/pkg/provider/... -run TestLiveXOProvisioning -v -timeout 15m
```

Add `XOA_LIVE_KEEP_TEMPLATE=1` to leave the built golden template in place afterward instead of deleting it (useful when you want to reuse it for a subsequent manual or Omni-driven test). It uses a real, current, no-extensions Talos schematic and version by default; override with `XOA_LIVE_SCHEMATIC`/`XOA_LIVE_TALOS_VERSION` if those defaults go stale.

This test is not run in CI (no live credentials are available there) and creates/deletes real infrastructure in whatever pool you point it at — don't point it at production.

What it does **not** cover — still validate manually before a release, ideally through a real Omni instance rather than by calling provider internals directly:

1. Fresh provider registration in Omni
2. Automatic first image import (golden-template build)
3. Reuse of the cached template
4. One-machine cluster creation
5. Three-control-plane cluster creation
6. Worker scale-up
7. Worker scale-down
8. Full deprovisioning without stale VIFs, disks, or VMs
9. Provider restart with active machines
10. Omni restart with active machines
11. Invalid pool ID failure
12. Invalid network ID failure
13. Xen Orchestra permission failure
14. Failed Image Factory URL/import behavior
15. Talos version change
16. System extension change
17. Manual template override

## Release recommendations

- Use semantic version tags.
- Publish immutable container tags and digests.
- Generate an SBOM and provenance attestation when possible.
- Sign container images with Cosign.
- Document supported Omni, Xen Orchestra, and XCP-ng versions in release notes.
- Never publish credentials in example files, logs, issues, or CI artifacts.

## Updating dependencies

Review changes in both Omni and the Xen Orchestra Go SDK before updating:

```bash
go get github.com/siderolabs/omni/client@<version>
go get github.com/vatesfr/xenorchestra-go-sdk@<version>
go mod tidy
go test ./...
```

Infrastructure-provider APIs may change between Omni releases, and the Xen Orchestra SDK's v1 (JSON-RPC) client may eventually be superseded by its v2 (REST) client as that matures. Treat dependency updates as compatibility changes and run the complete live test checklist.
