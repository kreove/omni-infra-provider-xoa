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

## Provider is not shown as connected in Omni

Check:

1. The Omni infrastructure-provider ID matches `--id`.
2. The service account key belongs to that provider ID.
3. `OMNI_ENDPOINT` includes the URL scheme.
4. The provider container can resolve and reach Omni.
5. TLS verification is succeeding.

Recreate credentials if necessary:

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

## VM is created but does not join Omni

Check the VM's console in Xen Orchestra and verify:

- The VM boots from the cloned boot disk instead of PXE.
- The VIF is connected to the intended network.
- DHCP or static addressing works.
- DNS and the default gateway are present.
- The VM can reach the Omni SideroLink and API endpoints.
- Firewall rules allow the ports configured by your Omni deployment.

The provider injects the Omni Machine Join Config as Xen Orchestra `cloudConfig` (delivered to the guest as NoCloud `user-data`).

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
