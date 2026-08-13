# Using the provider

## Provision a cluster

After the provider is connected and a Xen-Orchestra-backed Machine Class exists, create a cluster from the Omni UI or with a cluster template.

Example:

```yaml
kind: Cluster
name: xoa-production
kubernetes:
  version: v1.36.1
talos:
  version: v1.13.2
systemExtensions:
  - siderolabs/xen-guest-agent
---
kind: ControlPlane
machineClass:
  name: xoa-control-plane
  size: 3
---
kind: Workers
name: general
machineClass:
  name: xoa-workers
  size: 3
```

Replace the version values with versions offered and supported by your Omni instance.

```bash
omnictl cluster template validate -f cluster.yaml
omnictl cluster template sync -f cluster.yaml --verbose
omnictl cluster template status -f cluster.yaml
```

> [!NOTE]
> Omni cluster templates that use `machineClass` — which is how this provider is consumed — **do not support `kernelArgs`**. Setting `kernelArgs` at either cluster or machine-set level causes a validation error. This does not affect the serial-console argument the provider itself appends (`console=ttyS0,38400n8`), which is applied provider-side when generating the Image Factory schematic, not through the template.

Official cluster-template reference:

- <https://docs.siderolabs.com/omni/reference/cluster-templates>

## What happens during the first provisioning request

For the first machine using a given image identity, provisioning takes longer because the provider itself downloads, decompresses, and uploads the Talos raw image, then converts it into a template.

Provider log progression:

```text
starting Talos image import
created XO VM
machine is running
```

The `ensureImage` step retries with a short interval while the golden-template build runs in the background.

## Reusing an image

The cache identity includes:

- Image Factory base URL
- Omni schematic ID
- Talos version
- Architecture

Machines using the same combination reuse the same `omni-talos-*` template. Separate Machine Classes can therefore share an image while still using different CPU, memory, disk, network, pool, and SR settings.

## Scale a machine set

Change the `size` value in the cluster template and sync it again, or use the corresponding Omni UI controls.

Scale up:

```yaml
kind: Workers
name: general
machineClass:
  name: xoa-workers
  size: 5
```

Scale down:

```yaml
kind: Workers
name: general
machineClass:
  name: xoa-workers
  size: 2
```

When Omni releases a dynamically provisioned machine, the provider:

1. Powers off the VM.
2. Deletes its VIFs.
3. Deletes its disks.
4. Deletes the VM object.
5. Leaves shared cached golden templates in Xen Orchestra.

## Change Talos versions

Upgrade Talos through Omni. The new Talos version produces a different Image Factory URL and cache name. The provider builds the new template once, after which machines can reuse it during the rollout.

Official upgrade guide:

- <https://docs.siderolabs.com/omni/cluster-management/upgrading-clusters>

## Add or change system extensions

Configure extensions in Omni rather than pinning `template_id`. Extension changes produce a new schematic and therefore a new cached golden template.

See [Images and system extensions](images-and-extensions.md).

## Use different VM sizes

Create multiple provider-backed Machine Classes. For example:

### Control plane

```yaml
pool_id: "<xo-pool-uuid>"
sr_id: "<xo-sr-uuid>"
network_id: "<xo-network-uuid>"
architecture: amd64
cores: 4
memory: 8192
disk_size: 40
```

### General workers

```yaml
pool_id: "<xo-pool-uuid>"
sr_id: "<xo-sr-uuid>"
network_id: "<xo-network-uuid>"
architecture: amd64
cores: 8
memory: 16384
disk_size: 80
```

### Storage workers on another network

```yaml
pool_id: "<xo-pool-uuid>"
sr_id: "<xo-sr-uuid>"
network_id: "<other-xo-network-uuid>"
architecture: amd64
cores: 8
memory: 32768
disk_size: 160
```

## Move between pools, SRs, or networks

`pool_id`, `sr_id`, and `network_id` are evaluated when a VM is created. Changing them in a Machine Class does not migrate existing VMs. Replace or scale the machine set so Omni provisions new machines with the new values, then removes the old machines through its normal rollout and deprovisioning workflow.

## Provider restarts

Provisioning is designed to be idempotent:

- Existing VMs are found by Omni Machine Request name.
- Cached templates are found by deterministic name.
- Deletion tolerates resources that are already absent.

A provider restart resets the in-process image-build tracker; any build in progress at restart time is retried from scratch on the next reconciliation pass. Validate restart behavior in your environment before production use.

## Logs

Docker Compose:

```bash
docker compose logs -f --tail=200 omni-infra-provider-xoa
```

Kubernetes:

```bash
kubectl -n omni-infra-provider-xoa logs -f \
  deployment/omni-infra-provider-xoa
```

Useful log fields include the provider ID, Xen Orchestra endpoint, image URL, template ID, and VM ID.
