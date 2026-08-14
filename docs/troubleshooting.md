# Troubleshooting

Start with the provider logs:

```bash
docker compose logs -f --tail=200 omni-infra-provider-xoa
```

## `unsupported protocol scheme ""`

The endpoint is missing its URL scheme.

Wrong:

```dotenv
XOA_ENDPOINT=xoa.example.com
```

Correct:

```dotenv
XOA_ENDPOINT=wss://xoa.example.com
```

Check the actual container environment:

```bash
docker inspect omni-infra-provider-xoa \
  --format '{{range .Config.Env}}{{println .}}{{end}}' \
  | grep XOA_ENDPOINT
```

## Invalid Image Factory URL containing `$(`

Docker Compose uses `${VAR}` syntax, not `$(VAR)`.

Use:

```yaml
TALOS_IMAGE_FACTORY_BASE_URL: ${TALOS_IMAGE_FACTORY_BASE_URL:-https://factory.talos.dev}
```

Then recreate the container:

```bash
docker compose up -d --force-recreate omni-infra-provider-xoa
```

## `failed to decode service account key from options: illegal base64 data at input byte N`

The container starts, logs its startup line, then exits and restarts in a loop. The service account key is present but not valid base64.

The single most common cause, seen in practice, is an **extra `=` at the end** of the value — easy to introduce when copying the key, and easy to miss because base64 keys legitimately end in `=` or `==`. A valid key's length is a multiple of 4; one stray `=` makes it 1 over, and the decoder reports the position of that character.

Compare the padding against another working key:

```dotenv
OMNI_VSPHERE_SERVICE_ACCOUNT_KEY=eyJ...=
OMNI_XOA_SERVICE_ACCOUNT_KEY=eyJ...==   # <- one '=' too many
```

Check the length directly — it must divide by 4:

```bash
grep -m1 '^OMNI_XOA_SERVICE_ACCOUNT_KEY=' .env | sed 's/^[^=]*=//' | tr -d '\n' | wc -c
```

Note that line breaks are **not** a cause: Go's base64 decoder ignores `\r` and `\n`. Spaces, tabs, and quotes are fatal, and the provider strips whitespace from the key before decoding, so what remains is genuinely an invalid character.

Provider versions after `v0.1.0-alpha.1` diagnose this themselves, naming the offending character and its position instead of only reporting a byte offset. If you no longer have a clean copy of the key, issue a new one with `omnictl infraprovider renewkey xoa` — it is never displayed again after creation.

## Provider is not shown as connected in Omni

Check:

1. The Omni infrastructure-provider ID matches `--id`.
2. The service account key belongs to that provider ID, and the account is an **infrastructure provider** account (`infra-provider:xoa` with the `InfraProvider` role) rather than a plain service account.
3. `OMNI_ENDPOINT` includes the URL scheme.
4. The provider container can resolve and reach Omni.
5. TLS verification is succeeding.

Confirm the registration exists and what Omni thinks its status is:

```bash
omnictl infraprovider list
```

Recreate credentials if necessary — the key is only ever displayed at creation time, so this is the correct fix for a lost key (there is nowhere in the UI to read the existing one back):

```bash
omnictl infraprovider renewkey xoa
```

Check the exact syntax supported by your `omnictl` version with:

```bash
omnictl infraprovider --help
```

## Xen Orchestra returns `401` or authentication errors

The API token is invalid, expired, or lacks permission for the attempted operation. Xen Orchestra tokens and username/password sessions inherit the permissions of their associated user; check that user's effective role/ACLs for the target pool, SR, and network.

## Golden-template build fails at the disk-attach or convert-to-template step

The automatic image pipeline calls two Xen Orchestra JSON-RPC methods (`vm.attachDisk`, `vm.convertToTemplate`) that are not wrapped by the Go SDK. Both are confirmed working against a live pool (see [Compatibility and limitations](compatibility.md#findings-from-live-validation)), but if you're on a very different Xen Orchestra version:

1. Check the provider log for the exact error returned by Xen Orchestra for these calls.
2. Compare the parameters sent (in `internal/pkg/provider/image.go`, `importGoldenTemplate`) against what your Xen Orchestra version actually expects — use `xo-cli --list-commands` or the REST API Swagger UI (`<XO-URL>/rest/v0/docs`) to check current method signatures.
3. As an immediate workaround, build a template by hand following [Sidero's official Xen Orchestra guide](https://docs.siderolabs.com/talos/latest/platform-specific-installations/virtualized-platforms/xenorchestra) and reference it with `template_id` (manual mode), which doesn't depend on either call.
4. Delete any stray uploaded VDI or seed VM left behind by a failed build before retrying.

## `ensureImage` fails with "multiple XO templates named ... in pool ..."

XO's object sync for a freshly connected session can occasionally lag behind actual state, which has been observed to cause the provider to build a duplicate golden template instead of finding the existing one — see [Golden-template cache lookup can race with XO's own object sync](compatibility.md#golden-template-cache-lookup-can-race-with-xos-own-object-sync). List Xen Orchestra templates matching the name in the error, confirm they're genuinely duplicates of each other, and delete all but one.

## Image Factory returns `404`

Check:

- The Talos version requested by Omni exists in that Image Factory.
- The schematic was successfully generated.
- The factory base URL is correct.
- The architecture is `amd64`.
- The requested asset is `nocloud-amd64.raw.xz` (not `.qcow2`).
- A reverse proxy is not stripping the `/image/...` path.

## VM boots, console goes blank, then the VM cycles Running → Paused → Halted

`Running → Paused → Halted` in Xen Orchestra is XAPI's crash handling: it pauses the domain to capture a coredump, then destroys it. The guest is crashing, not failing to boot.

**Check the hypervisor's log first** — a guest that dies before its kernel console initializes produces no output anywhere a console can show, and the reason is only visible on the XCP-ng host:

```bash
xl dmesg | tail -60
```

The one instance of this investigated so far (provider `v0.1.0-alpha.2` and earlier) showed:

```text
p2m_pod_demand_populate: Dom79 out of PoD memory! (tot=66075 ents=983008)
domain_crash called from p2m_pod_demand_populate
```

Root cause: the provider set the VM's *static* memory maximum but not its *dynamic* memory range, which stayed at the 256 MiB inherited from the "Other install media" seed template. XAPI boots an HVM guest whose dynamic memory is below its static maximum in Xen **populate-on-demand** mode — the guest sees the full static-max but only the dynamic amount is actually backed, and it is expected to balloon down before touching the rest. Talos has no balloon driver running in early boot and touches memory immediately (`init_on_alloc=1`), so Xen crashed every VM within seconds. Fixed in the release after `v0.1.0-alpha.2` by pinning dynamic-min = dynamic-max = static-max, which disables PoD (the same thing XO's own VM-creation UI does).

VMs created by an affected version can be repaired in place while Halted — the provider's reconcile loop powers them back on automatically:

```bash
xe vm-list name-label=<vm-name> params=uuid --minimal
xe vm-memory-limits-set uuid=<UUID> static-min=134217728 \
  dynamic-min=<BYTES> dynamic-max=<BYTES> static-max=<BYTES>
# <BYTES> = the Machine Class memory, e.g. 4294967296 for 4 GiB
```

If the XO console shows the Talos boot entry and `Booting the kernel (entry_offset: ...)` and then goes blank, the boot chain is fine — the disk, bootloader, partition table, and firmware are all working. What you are missing is everything the kernel printed afterwards.

Provider versions up to and including `v0.1.0-alpha.1` set only `console=ttyS0,38400n8` as an extra kernel argument (inherited from the VergeOS provider, which enabled a serial port on its VMs). XCP-ng HVM guests have no serial port unless one is configured, so `/dev/console` pointed at a device that did not exist and every later message — including the panic — was discarded. Later versions append `console=tty0` so output lands on the console XO shows you.

Upgrade the provider, let it build a new golden template (changing kernel arguments changes the Image Factory schematic, so a fresh image is downloaded), and read the panic off the console.

Things worth ruling out before assuming a provider bug, since none of them were the cause in the one case investigated so far:

| Check | How |
| --- | --- |
| Memory | Talos needs 2 GiB for a control plane node, 1 GiB for a worker |
| Disk | 10 GiB minimum |
| CPU | Talos requires `x86-64-v2` (Nehalem, 2008, or newer) |
| Storage | The SR must not be full |
| Image integrity | Export the template VDI and confirm it starts with an MBR signature and an `EFI PART` GPT header |

## Harmless messages in a healthy boot

These appear on a working machine and need no action:

| Message | Why |
| --- | --- |
| `GPT: Alternate GPT header not at the end of the disk` | The boot disk is a clone of the golden template resized to `disk_size`, which leaves the backup GPT at the template's old end. Talos repartitions moments later and rewrites the table, so this only appears on a machine's first boot. |
| `kvm_intel: VMX not supported` / `kvm_amd: CPU isn't AMD` | Nested virtualization is not exposed to a Xen HVM guest. |
| `unsupported CPU family ... no PMU driver` | Xen does not pass the host PMU through. |
| `hyperv_fb`, `hv_vmbus`, `hv_netvsc`, `hv_balloon` registering | Talos ships Hyper-V drivers; they register and stay idle on Xen. |
| `ipmi_si: Unable to find any System Interface(s)` | No BMC in a VM. |
| `avc: denied ... permissive=1` | Talos runs SELinux in permissive mode; these are logged, not enforced. |
| `EventsSinkController ... no machines found for address fdae:...` | Transient, while SideroLink registration completes. |
| `NodeApplyController ... node not found` | Transient, until the node registers with the Kubernetes API server. |
| `etcd ... rpc not supported for learner` | Normal etcd learner promotion; followed by `successfully promoted etcd member`. |
| `TPM device is not available` | XCP-ng guests have no vTPM, so TPM-backed disk encryption is unavailable. |

A healthy boot ends with `machine is running and ready`.

## Talos boots but stays in maintenance mode and never joins Omni

The dashboard shows `STAGE: Maintenance`, Omni shows the machine as "Provisioned / Waiting for the machine to join Omni", and the Talos log contains:

```text
downloading config {"controller": "config.AcquireController", "platform": "nocloud"}
volume status {"volume": "platform/cidata/config", "phase": "waiting -> missing"}
entering maintenance service
```

Talos could not find the NoCloud config drive. Ask the machine what it can see — a node in maintenance mode answers insecure API calls:

```bash
talosctl get discoveredvolumes -e <node-ip> -n <node-ip> --insecure
```

A working config drive appears as a partition or disk with `vfat` and the label `cidata`. If the config drive shows up as a bare `disk` with no type and no label, Talos cannot read it.

This was the behaviour of every VM created by `v0.1.0-alpha.2` and earlier, which asked Xen Orchestra to build the drive via the `cloudConfig` parameter. XO's drive has the right contents and the right `cidata` label, but it wraps the FAT16 filesystem in an MBR whose only partition begins at LBA 1. That layout is ambiguous with a whole-disk filesystem, so Talos's prober rejects the partition table and then finds only the MBR at offset 0. Later versions build the drive themselves — a plain FAT16 at offset 0 — and attach it before the first power-on.

If you need to confirm what is on a config drive, export the VDI and inspect it:

```bash
curl -k -H "Cookie: authenticationToken=$XOA_TOKEN" \
  "https://xoa.example.com/rest/v0/vdis/<vdi-uuid>.raw" -o cfg.img
file cfg.img          # a readable drive reports a FAT filesystem, not "data"
```

> [!WARNING]
> Only export a VDI whose VM is **halted**. Exporting an attached VDI can leave it flagged "not detached cleanly", after which the VM fails to start with `SR_BACKEND_FAILURE_46`. Recover with `xe-toolstack-restart` on the host that owns the VM, or by removing the affected disk.

## VM is created but does not join Omni

Check the VM's console in Xen Orchestra and verify:

- The VM boots from the cloned boot disk instead of PXE.
- The VIF is connected to the intended network.
- DHCP or static addressing works.
- DNS and the default gateway are present.
- The VM can reach the Omni SideroLink and API endpoints.
- Firewall rules allow the ports configured by your Omni deployment.

The provider builds a NoCloud config drive containing the Omni Machine Join Config and attaches it as a disk named `cidata` before the VM is first powered on.

## Duplicate VM name

The provider expects exactly one Xen Orchestra VM per Omni Machine Request name (`name_label`). If multiple VMs share the same name, provisioning fails deliberately.

Remove or rename the unmanaged duplicate. Do not arbitrarily delete the VM that is currently registered with Omni.

## Deprovisioning is stuck

The provider removes resources in this order:

1. Power off VM
2. Delete VIFs
3. Delete disks
4. Delete VM

Inspect the logs for the exact failing object and verify delete permission.

## TLS certificate errors

Preferred fixes:

- Use a certificate issued by a CA trusted by the container.
- Build a runtime image containing your internal CA.

Temporary test options:

```yaml
command:
  - --xoa-insecure-skip-verify
  - --insecure-skip-verify
```

The first flag affects Xen Orchestra. The second affects Omni.

## Collect information for an issue

Include:

- Provider release tag or commit (run `omni-infra-provider-xoa --version`, or read the `version` field in the startup log line)
- Omni version
- Xen Orchestra and XCP-ng version
- Talos version
- Sanitized Machine Class provider data
- Relevant provider logs
- Whether the image was automatic or specified by `template_id`

Remove service-account keys, API tokens, join tokens, internal credentials, and other secrets before posting logs.
