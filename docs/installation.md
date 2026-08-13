# Installation

This guide deploys the provider as a Docker Compose service. A Kubernetes example is also included in `deploy/kubernetes.yaml`.

## 1. Register an infrastructure provider in Omni

The provider registers under the ID `xoa` by default. The Omni infrastructure-provider service account must use the same ID.

Using `omnictl`:

```bash
omnictl infraprovider create xoa
```

The command returns values similar to:

```dotenv
OMNI_ENDPOINT=https://omni.example.com
OMNI_SERVICE_ACCOUNT_KEY=eyJ...
```

> [!IMPORTANT]
> **The service account key is printed once, at creation, and is never retrievable afterwards.** It is not shown anywhere in the Omni UI later — not under **Settings → Infra Providers** and not under **Settings → Service Accounts**. Copy it out of the command output (or the UI dialog) immediately and store it as a secret.
>
> If you have already lost it, do not delete and recreate the provider — issue a new key instead:
>
> ```bash
> omnictl infraprovider renewkey xoa
> ```
>
> That prints a fresh `OMNI_SERVICE_ACCOUNT_KEY` for the same provider ID and invalidates the previous one.

To use another provider ID, start the binary with `--id <provider-id>` and create the Omni infrastructure provider using that same ID.

Useful related commands:

```bash
omnictl infraprovider list          # IDs, connection status, and errors
omnictl infraprovider delete xoa    # remove a provider registration
```

### Creating the service account directly

`omnictl infraprovider create xoa` is a convenience wrapper: underneath it creates a service account named `infra-provider:xoa` with the `InfraProvider` role. That is why the account shows up as `infra-provider:xoa`, not `xoa`, if you go looking for it in the UI.

You can create it explicitly instead, which is the form the Sidero KubeVirt provider documents:

```bash
omnictl serviceaccount create --use-user-role=false --role=InfraProvider infra-provider:xoa
```

Both approaches are equivalent. A *plain* service account created without `--role=InfraProvider` — or without the `infra-provider:` name prefix — will authenticate but will not be allowed to act as an infrastructure provider, so make sure you created the right kind.

For multiple independent Xen Orchestra instances, run one provider instance per instance and give each instance a unique provider ID and Omni service account.

Official Omni reference:

- <https://docs.siderolabs.com/omni/infrastructure-and-extensions/infrastructure-providers>
- <https://docs.siderolabs.com/omni/reference/cli>

## 2. Create a Xen Orchestra account for the provider

Create a dedicated Xen Orchestra user for the provider rather than using an administrator's personal account.

The provider can authenticate either with an API token (preferred) or with that user's username and password. Set **one** of these in `deploy/.env`:

```dotenv
XOA_TOKEN=...
# or
XOA_USERNAME=...
XOA_PASSWORD=...
```

If a token is set, the username and password are ignored.

### Getting a token

There is no "API tokens" page under **Settings** in the Xen Orchestra UI. Token creation lives on your **own user page**, and the exact location moves between XO 5 and XO 6, so the reliable, version-independent way is the CLI:

```bash
# Same arguments as `xo-cli register`; prints the token to stdout.
xo-cli create-token https://xoa.example.com provider-user
```

Run this while logged in **as the provider's user**, not as your admin account — the token inherits the permissions of whoever created it.

In the XO 5 web UI, the equivalent lives in your user space rather than in Settings: click your username at the bottom of the left sidebar to open the **User** page, then look for the authentication-tokens section. If you cannot find it in your version, use `xo-cli create-token` or the REST endpoint `POST /rest/v0/users/me/authentication_tokens` instead.

Username and password authentication is fully supported and is a perfectly reasonable fallback if tokens are awkward in your version — it is what the provider's live validation was performed with. The trade-off is that the credentials are longer-lived and higher-value than a scoped token, so restrict the account's permissions accordingly and rotate it if it leaks.

### Required permissions

The provider performs the following operations:

| Resource | Required operations |
| --- | --- |
| Pools | list/read |
| Networks | list/read |
| Storage repositories | list/read |
| VDIs | list/read/create/delete, raw upload |
| Virtual machines | list/read/create/modify/delete and power operations |
| VIFs | list/read/create/delete |
| Templates | list/read, convert-to-template |

Permission names and scopes can differ between Xen Orchestra releases and depend on the ACL/RBAC model in use. Start with permissions scoped to the target pool, SR, and network, then reduce them after validating all lifecycle operations.

Official Xen Orchestra references:

- <https://docs.xen-orchestra.com/xo5/restapi>
- <https://docs.xcp-ng.org/management/manage-at-scale/xo-api/>

## 3. Record the Xen Orchestra object IDs

Each Machine Class needs:

- `pool_id`: the Xen Orchestra pool UUID
- `sr_id`: the Xen Orchestra Storage Repository UUID
- `network_id`: the Xen Orchestra network UUID

Find these in the Xen Orchestra UI (each object's detail page shows its UUID) or via `xo-cli --list-objects`.

Confirm that the selected network provides the addressing, DNS, gateway, and routing needed by Talos VMs to reach Omni, and that the selected SR has enough free space for cached golden templates plus every VM's boot disk.

## 4. Obtain the provider image

### Use a published image

Set the full image reference in `deploy/.env`:

```dotenv
PROVIDER_IMAGE=ghcr.io/kreove/omni-infra-provider-xoa:VERSION
```

Pin a release tag or digest in production. Do not rely on `latest` for controlled upgrades.

Published images are built for `linux/amd64` and `linux/arm64`, ship an SBOM and provenance attestation, and are signed with cosign. Each release's notes carry the immutable digest — prefer pinning that:

```dotenv
PROVIDER_IMAGE=ghcr.io/kreove/omni-infra-provider-xoa@sha256:...
```

Verify the signature before deploying:

```bash
cosign verify "ghcr.io/kreove/omni-infra-provider-xoa@sha256:..." \
  --certificate-identity-regexp "^https://github.com/kreove/omni-infra-provider-xoa/" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### Build locally

From the repository root:

```bash
docker build --pull --no-cache \
  -t omni-infra-provider-xoa:local \
  .
```

Then set:

```dotenv
PROVIDER_IMAGE=omni-infra-provider-xoa:local
```

## 5. Configure Docker Compose

```bash
cp deploy/example.env deploy/.env
chmod 600 deploy/.env
```

Populate `deploy/.env`:

```dotenv
PROVIDER_IMAGE=omni-infra-provider-xoa:local
OMNI_ENDPOINT=https://omni.example.com
OMNI_SERVICE_ACCOUNT_KEY=replace-me
XOA_ENDPOINT=wss://xoa.example.com
XOA_TOKEN=replace-me
TALOS_IMAGE_FACTORY_BASE_URL=https://factory.talos.dev
```

Start the service:

```bash
cd deploy
docker compose up -d
docker compose ps
docker compose logs -f omni-infra-provider-xoa
```

Expected startup log fields include:

```text
starting Xen Orchestra infrastructure provider
provider_id=xoa
xoa_endpoint=wss://xoa.example.com
image_factory_base_url=https://factory.talos.dev
```

## 6. Verify connectivity

Confirm the exact environment received by the container:

```bash
docker inspect omni-infra-provider-xoa \
  --format '{{range .Config.Env}}{{println .}}{{end}}' \
  | grep -E '^(OMNI_ENDPOINT|XOA_ENDPOINT|TALOS_IMAGE_FACTORY_BASE_URL)='
```

The provider needs outbound connectivity to:

- The Omni API endpoint
- The Xen Orchestra endpoint
- The Talos Image Factory endpoint (the provider downloads images itself; Xen Orchestra does not proxy this download)

The provisioned Talos VMs must be able to reach the Omni addresses and ports configured for your instance. Use the official Omni firewall requirements for your hosted or self-hosted deployment:

- <https://docs.siderolabs.com/omni/omni-cluster-setup/omni-firewall-egress-requirement>

## 7. Create the Machine Class

In Omni, open the registered Xen Orchestra infrastructure provider and create a dynamic Machine Class or machine request. Omni renders the provider-specific form from the schema published by this service.

Recommended provider data:

```yaml
pool_id: "<xo-pool-uuid>"
sr_id: "<xo-sr-uuid>"
network_id: "<xo-network-uuid>"
architecture: amd64
cores: 4
memory: 8192
disk_size: 32
```

Leave `template_id` unset for automatic image selection.

## 8. Run a smoke test

Start with one disposable control-plane machine and no workers. Watch both systems:

```bash
docker compose logs -f omni-infra-provider-xoa
```

In Xen Orchestra, monitor Tasks and the Disks/Templates/VMs lists. The expected first-time sequence is:

1. The provider registers the machine request.
2. The provider downloads and decompresses the Talos raw image, uploads it as a VDI, and converts it into an `omni-talos-*` template.
3. The provider creates the VM by fast-cloning that template.
4. The provider adds a VIF on the selected network.
5. The provider powers on the VM.
6. The Talos machine connects to Omni.

Once that works, test scale-up and scale-down before using the provider for important clusters.

## Kubernetes deployment

Edit the image and Secret values in `deploy/kubernetes.yaml`, then apply it:

```bash
kubectl apply -f deploy/kubernetes.yaml
kubectl -n omni-infra-provider-xoa logs -f deployment/omni-infra-provider-xoa
```

Run exactly one replica for a provider ID. The current in-process image-build tracker is not distributed between replicas.
