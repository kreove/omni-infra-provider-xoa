# Compatibility and limitations

## Release status

This provider is a community alpha, ported from [omni-infra-provider-vergeos](https://github.com/kreove/omni-infra-provider-vergeos). It builds, passes unit tests, and has been validated end-to-end against a live XCP-ng pool managed by Xen Orchestra: golden-template build, template-cache reuse, VM clone with correctly resized disk and VIF, cloud-init delivery, power-on, and full deprovisioning. See [Findings from live validation](#findings-from-live-validation) for what that testing found and fixed, and what's still open. It has not yet been driven through an actual Omni instance (real Machine Requests, scale-up/down), and it has not been certified by Sidero Labs or Vates.

## Build-time dependencies

The current source tree declares:

- Go `1.26.2`
- Omni client `v1.8.0`
- Xen Orchestra Go SDK `v1.18.0`

See `go.mod` for the authoritative dependency versions.

## Platform support

| Capability | Status |
| --- | --- |
| `amd64` | Supported |
| `arm64` | Not supported (XCP-ng is x86-only) |
| UEFI / boot firmware | Forced to UEFI, matching Sidero's Xen Orchestra guide; not a per-Machine-Class setting |
| Secure Boot | Not configured by the provider (defaults to the template's setting) |
| NoCloud join config via `cloudConfig`/`networkConfig` | Supported |
| Automatic Image Factory import | Supported and validated live; relies on two unwrapped XO JSON-RPC calls — see below |
| Existing Xen Orchestra template override | Supported |
| Multi-network Machine Classes | Supported through Machine Class data |
| Multiple XCP-ng pools | Supported through Machine Class data |
| Multiple independent Xen Orchestra instances | Run separate provider IDs/instances |
| Distributed provider replicas | Not supported; run one replica per provider ID |
| Automatic cached-template garbage collection | Not implemented |
| Authenticated Image Factory headers/tokens | Not implemented |
| Existing VM CPU/RAM resize reconciliation | Not implemented |
| Existing VM network migration | Not implemented |

## Fields dropped from the VergeOS provider

These VergeOS Machine Class fields have no direct Xen Orchestra/XCP-ng equivalent and were removed rather than faked:

| Field | Why it was dropped |
| --- | --- |
| `cpu_type` | XCP-ng/Xen doesn't expose QEMU-style CPU model pinning the way VergeOS's QEMU backend does |
| `machine_type` | No QEMU machine-type concept on Xen |
| `disk_interface` | Xen VBDs don't offer the same per-device interface choice (virtio-scsi/nvme/ahci) |
| `network_interface` | Xen VIFs don't offer the same per-device interface choice (virtio/e1000/vmxnet3) |
| `uefi` | Determined by the golden/manual template rather than settable per Machine Class in the current implementation, even though the underlying `vm.create` call does support a `hvmBootFirmware` parameter — a per-Machine-Class `uefi` field could be added later without architectural changes |
| `guest_agent` | No equivalent VM-level toggle; maps to installing the `siderolabs/xen-guest-agent` Talos extension instead (see [Images and system extensions](images-and-extensions.md)) |

## Findings from live validation

The automatic image pipeline's disk-attach and convert-to-template steps use two Xen Orchestra JSON-RPC methods not wrapped by the official Go SDK, called through the client's raw `Call` escape hatch. Both are now confirmed against a live instance:

- `vm.attachDisk` (params `vm`, `vdi`; returns a bool) — attach the freshly-imported VDI to a seed VM. An earlier version of this code guessed the raw XAPI-style name `VBD.create`, which doesn't exist as a client-callable JSON-RPC method (`VBD.create` is XAPI-internal, used by xo-server's own backend, not exposed to clients — the client-facing equivalent is `vm.attachDisk`).
- `vm.convertToTemplate` (param `id`) — convert that seed VM into a clonable template. This one was right on the first guess.

Live testing also found and fixed three real gaps between what the Go SDK assumes and what XO's JSON-RPC API actually expects, none of which are specific to this provider's design:

- **`CreateVm` fails if `XenstoreData` is left as a nil Go map.** The SDK always sends `xenStoreData` in a follow-up `vm.set` call; a nil map marshals to JSON `null`, which newer XO versions reject (`must be object`). Fixed by passing an explicit empty map.
- **`CreateVm` requires an explicit target `PowerState`.** It always waits for a target power state after creation and treats an unset (empty-string) `PowerState` as invalid, rather than "don't wait." Fixed by explicitly requesting `HaltedPowerState` (the provider powers the VM on itself in a later `syncMachine` retry, same as before).
- **The SDK's `DeleteVIF` unconditionally disconnects before deleting**, but XO rejects disconnecting a VIF that's already unattached — which every VIF on a halted VM is. Since `Deprovision` only reaches VIF cleanup after confirming the VM is halted, this failed on every deprovision. Fixed by calling `vif.delete` directly, skipping the disconnect.

One further live finding changed the deprovisioning approach rather than fixing a bug in this codebase: the SDK's `HaltVm` (used for the standard "power off" step) refuses to even attempt a stop RPC until XO reports PV drivers detected in the guest, with no fallback ([known upstream issue](https://github.com/vatesfr/terraform-provider-xenorchestra/issues/220)). A machine being deprovisioned may never have booted successfully — bad join config, a crashed Talos install, a missing `xen-guest-agent` extension — so deprovisioning can't depend on PV drivers being present. `Deprovision` therefore calls `vm.stop` with `force: true` directly instead of going through `HaltVm`.

## Image import readiness

A build is considered complete once the seed VM has been successfully converted into a template. A failed build (a download, upload, attach, or convert-to-template error) surfaces as a provisioning error on the `ensureImage` step and does not leave a permanently cached bad entry — the next Machine Request retries the whole build from scratch. A failed build may still leave a stray uploaded VDI or seed VM in Xen Orchestra that must be cleaned up manually.

## Golden-template cache lookup can race with XO's own object sync

Live testing observed that `GetTemplate(NameLabel: cacheName, PoolId: ...)`, run from a freshly established connection, can report "not found" for a template that genuinely exists and was built minutes earlier by a different process/connection — apparently because XO's `xo.getAllObjects` collection sync for a new session can lag behind actual state. This is an XO/session behavior, not something this provider's code controls. In practice it caused a couple of duplicate `omni-talos-*` templates to accumulate during testing.

Consequences:

- A provider that restarts shortly after building a golden template may occasionally rebuild a duplicate rather than reusing the one it just made, wasting a download/upload cycle.
- If duplicates do accumulate, `ensureTalosImage` deliberately surfaces this as an explicit error (`multiple XO templates named %q in pool %q`) rather than silently picking one, so it's visible rather than silently wrong.

If you see that error, list templates matching the reported name and delete all but one (see [Cache cleanup](images-and-extensions.md#cache-cleanup)).

## Image cache lifecycle

Cached `omni-talos-*` templates persist after machines are deleted. This is intentional and improves subsequent provisioning speed. Operators must currently clean unused templates manually.

## Existing-machine changes

Machine Class changes to the following fields affect newly created VMs only:

- CPU cores
- Memory
- Disk size
- Network
- Pool
- Storage repository

To apply these changes, use an Omni rollout that provisions replacement machines and deprovisions the old ones.

## Omni and Talos compatibility

Select Talos and Kubernetes versions through Omni. Use versions supported by your Omni release and follow Omni's supported upgrade paths.

The provider uses APIs from the Omni client version declared in `go.mod`. A new Omni release may require rebuilding or updating the provider dependencies.

## Xen Orchestra / XCP-ng compatibility

Validated live against one current XCP-ng pool managed by Xen Orchestra. No formal minimum Xen Orchestra or XCP-ng release is declared beyond that. The target environment must support the API operations used by `xenorchestra-go-sdk v1.18.0` and by the raw JSON-RPC calls noted above, including:

- Username/password **or** API token authentication — both exercised against a live instance. (Tokens are not created from a Settings page in the XO UI; see [Installation](installation.md#getting-a-token).)
- VM create/read/delete and power operations, including `vm.stop` with `force: true`
- VDI upload (REST VDI import) and lifecycle operations
- VIF lifecycle operations, including `vif.delete` without a prior disconnect
- `vm.attachDisk` and `vm.convertToTemplate`
- NoCloud cloud-init via `cloudConfig`/`networkConfig`

Report the exact Xen Orchestra and XCP-ng release when filing compatibility issues.
