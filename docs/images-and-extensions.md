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

Configure it **in Omni**. The provider has no Image Factory setting of its own: it asks Omni for the installation medium it wants and Omni returns a URL, along with any headers needed to fetch it. A self-hosted or authenticated factory is therefore a matter of how Omni is set up, and the provider follows automatically.

The provider container must still trust the factory's TLS certificate and be able to resolve and reach the hostname, since it performs the download itself. Authentication that Omni expresses as request headers or as a token inside the URL is now handled: the provider sends whatever headers Omni returns.

Official self-hosted Image Factory guide:

- <https://docs.siderolabs.com/omni/self-hosted/run-image-factory-on-prem>

## Cache cleanup

The provider intentionally does not delete cached golden templates during VM deprovisioning because they may be shared by other machines or future scale-up operations.

For now, clean unused templates manually.

**Identifying a template.** The name is a digest, so it says nothing on its own. Each template's *description* records the Talos version, architecture and schematic:

```text
Talos v1.12.4 amd64, schematic <schematic>, golden image managed by Sidero Omni
```

The download URL is deliberately not recorded, because it can carry credentials.

> [!WARNING]
> A template records the version a machine was **created** at, which is not necessarily the version it **runs**. Talos upgrades are applied in place — Omni writes the new version to the machine's alternate boot partition over SideroLink, and the infrastructure provider is not involved — so after an upgrade a machine still descends from the template it was originally cloned from while running something newer. Comparing a template's version against a running cluster is therefore not a reliable way to spot a stale template.

**Deciding what is safe to remove.** Only one test is reliable: does any live VM's boot disk descend from it? Machines are fast clones, so their disks are copy-on-write children of the template's VDI. Follow each VM's `disk0` chain through its VDI `parent` links to the root and compare that against the template's VDI. Anything with no live descendants is safe to delete, whatever its version says.

Two things make templates look staler than they are:

- Two clusters on different Talos versions legitimately keep two templates.
- After an upgrade, machines still pin the template they were created from, even though its version now looks out of date.

Scaling up a cluster that has been upgraded creates machines from a template built for the *new* version, while the original machines still descend from the old one. A single cluster can therefore legitimately span several templates, none of which can be removed until the machines pinning them are gone.

**Removing one.** Delete the *template*, not just its disk — removing the VDI alone leaves a broken template object behind. Deleting a template does not disturb VMs already cloned from it; the storage layer keeps the shared base and coalesces in the background.

Automatic garbage collection is not implemented.

## Guest-agent extension

Install the `siderolabs/xen-guest-agent` Talos system extension in Omni so Xen Orchestra reports the VM's IP addresses and other guest information. Unlike VergeOS, this is not a provider-side VM setting — it's purely a matter of which extensions Omni includes in the schematic, exactly as with any other Talos system extension.
