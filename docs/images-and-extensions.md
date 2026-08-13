# Images and system extensions

## Image selection is controlled by Omni

In automatic mode, the provider does not select a fixed generic Talos template. Omni supplies:

- The Talos version requested by the cluster
- The resolved Image Factory schematic
- The machine join configuration

The schematic represents image-affecting configuration such as system extensions. The provider builds this URL:

```text
https://factory.talos.dev/image/<schematic>/<talos-version>/nocloud-amd64.raw.xz
```

The configured Image Factory base URL may be public or self-hosted.

Unlike VergeOS, Xen Orchestra has no server-side URL-import feature, and XCP-ng's QCOW2 storage path is not production-ready. The provider therefore downloads and decompresses the `.raw.xz` asset itself before uploading it into Xen Orchestra.

## Cache identity

The complete image URL is hashed into a deterministic name:

```text
omni-talos-<24-hex-character-hash>
```

A new golden template is built when any of these change:

- Schematic
- Talos version
- Architecture
- Image Factory base URL

Cached templates are shared across machines and clusters within the same pool using the same image identity.

## Golden-template build sequence

The first Machine Request for a new image identity triggers this sequence (see `internal/pkg/provider/image.go`):

1. Download and decompress the `nocloud-amd64.raw.xz` asset to a scratch file on the provider host.
2. Upload the decompressed raw disk into the configured Storage Repository as a new VDI (`disk.create`/REST VDI import, wrapped by the Go SDK's `CreateVDI`).
3. Create a diskless seed VM from Xen Orchestra's built-in "Other install media" template.
4. Attach the uploaded VDI to the seed VM as its boot disk.
5. Convert the seed VM into a template.

Steps 4 and 5 use Xen Orchestra JSON-RPC methods (`vm.attachDisk`, `vm.convertToTemplate`) that are not wrapped by the official Go SDK; both are confirmed working against a live pool — see [Compatibility and limitations](compatibility.md#findings-from-live-validation) for what live testing found and fixed. If either call behaves differently on your Xen Orchestra version, this is the first place to look.

Subsequent machines using the same image identity skip straight to fast-cloning the cached template's disk.

## First use of a new schematic

1. Omni creates a Machine Request with a schematic and Talos version.
2. The provider searches for an existing template with the deterministic cache name in the configured pool.
3. If absent, the provider runs the golden-template build sequence above in the background and returns a short retry interval while it's in progress.
4. Once built, the provider creates the VM by fast-cloning the template's disk.

## Manual template override

Set `template_id` only when you intentionally want to bypass automatic selection:

```yaml
template_id: "33333333-3333-3333-3333-333333333333"
```

With a manual override:

- The selected Xen Orchestra template is used for every Machine Request using that Machine Class.
- Changes to Talos version or extensions do not change the selected template.
- The provider does not validate that the template contains the requested extensions.
- The operator is responsible for template lifecycle and compatibility.

Manual mode is useful for testing, disconnected environments, or emergency rollback, but automatic mode is recommended for normal Omni-managed clusters. To build a template by hand, follow [Sidero's official Xen Orchestra guide](https://docs.siderolabs.com/talos/latest/platform-specific-installations/virtualized-platforms/xenorchestra), which uses the same underlying image and a very similar manual version of the automatic sequence above.

## Self-hosted Image Factory

Set:

```dotenv
TALOS_IMAGE_FACTORY_BASE_URL=https://factory.example.com
```

The provider appends `/image/<schematic>/<version>/nocloud-<architecture>.raw.xz` to this base URL.

The provider container must trust the factory's TLS certificate and be able to resolve and reach the hostname. The current provider has no separate Image Factory authentication settings. Private factories that require custom request headers or tokens are not supported by this release.

Official self-hosted Image Factory guide:

- <https://docs.siderolabs.com/omni/self-hosted/run-image-factory-on-prem>

## Cache cleanup

The provider intentionally does not delete cached golden templates during VM deprovisioning because they may be shared by other machines or future scale-up operations.

For now, clean unused templates manually:

1. Filter Xen Orchestra templates for names beginning with `omni-talos-`.
2. Confirm no VM was cloned from the template that is still in use (templates themselves aren't directly "referenced," but treat any recently-used cache name as live).
3. Retain templates required for active clusters or rollback.
4. Delete only templates that are no longer needed.

Automatic garbage collection is not implemented.

## Guest-agent extension

Install the `siderolabs/xen-guest-agent` Talos system extension in Omni so Xen Orchestra reports the VM's IP addresses and other guest information. Unlike VergeOS, this is not a provider-side VM setting — it's purely a matter of which extensions Omni includes in the schematic, exactly as with any other Talos system extension.
