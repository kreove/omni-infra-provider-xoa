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

Store the key as a secret. To use another provider ID, start the binary with `--id <provider-id>` and create the Omni infrastructure provider using that same ID.

For multiple independent Xen Orchestra instances, run one provider instance per instance and give each instance a unique provider ID and Omni service account.

Official Omni reference:

- <https://docs.siderolabs.com/omni/infrastructure-and-extensions/infrastructure-providers>
- <https://docs.siderolabs.com/omni/reference/cli>

## 2. Create a Xen Orchestra service account and API token

Create a dedicated Xen Orchestra user for the provider rather than using an administrator's personal account. Generate an API token for that user (**Settings → API tokens** in the Xen Orchestra UI, or `xo-cli create-token`) and store it securely.

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
