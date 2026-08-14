# Configuration reference

The provider accepts environment variables and equivalent command-line flags. Command-line flags take precedence over environment-derived defaults.

## Provider configuration

| Environment variable | Flag | Required | Default | Description |
| --- | --- | ---: | --- | --- |
| `OMNI_ENDPOINT` | `--omni-api-endpoint` | Yes | none | Omni API base URL |
| `OMNI_SERVICE_ACCOUNT_KEY` | `--omni-service-account-key` | Normally | none | Infrastructure-provider service account key |
| n/a | `--id` | No | `xoa` | Provider ID registered in Omni |
| n/a | `--provider-name` | No | `Xen Orchestra` | Display name in Omni |
| n/a | `--provider-description` | No | alpha description | Display description in Omni |
| `XOA_ENDPOINT` or `XOA_HOST` | `--xoa-endpoint` | Yes | none | Xen Orchestra base URL, e.g. `wss://xoa.example.com` |
| `XOA_TOKEN` | `--xoa-token` | One auth method | none | Preferred Xen Orchestra authentication method |
| `XOA_USERNAME` | `--xoa-username` | One auth method | none | Username when token auth is not used |
| `XOA_PASSWORD` | `--xoa-password` | One auth method | none | Password when token auth is not used |
| `TALOS_IMAGE_FACTORY_BASE_URL` | `--image-factory-base-url` | No | `https://factory.talos.dev` | Public or private Image Factory base URL |
| n/a | `--xoa-insecure-skip-verify` | No | `false` | Skip Xen Orchestra TLS verification |
| n/a | `--insecure-skip-verify` | No | `false` | Skip Omni TLS verification |

### TLS

Use trusted certificates whenever possible. The insecure flags disable certificate verification and should be limited to temporary tests or isolated environments.

For an internal certificate authority, the better approach is to add its CA certificate to a custom runtime image rather than disabling verification.

### Authentication priority

The provider uses Xen Orchestra credentials in this order:

1. `XOA_TOKEN`
2. `XOA_USERNAME` and `XOA_PASSWORD`

If a token is set, username/password values are ignored.

## Machine Class provider data

The schema is defined in `internal/pkg/provider/data/schema.json` and registered with Omni at startup.

| Field | Required | Default | Description |
| --- | ---: | --- | --- |
| `pool_id` | Yes | none | Xen Orchestra pool UUID |
| `sr_id` | Yes | none | Xen Orchestra Storage Repository UUID used for cached images and VM disks |
| `network_id` | Yes | none | Xen Orchestra network UUID attached to the VM's primary VIF |
| `template_id` | No | unset | Existing Xen Orchestra template UUID; unset enables automatic images |
| `architecture` | Yes in schema | `amd64` | Only `amd64` is currently supported |
| `cores` | Yes | UI default `4` | Virtual CPU core count |
| `memory` | Yes | UI default `8192` | Memory in MiB; minimum 2048. Allow headroom — see below |
| `disk_size` | Yes | UI default `32` | Boot disk size in GiB; minimum 5 |

Fields present in the VergeOS provider that have no direct Xen Orchestra equivalent — `cpu_type`, `machine_type`, `disk_interface`, `network_interface`, `uefi`, `guest_agent` — were intentionally dropped rather than faked. See [Compatibility and limitations](compatibility.md) for why.

### Sizing memory

`memory` is what the VM is given, not what Talos sees. Firmware and hypervisor reservations take roughly 200 MiB off the top, so a machine configured with 4096 MiB reports about 3889 MiB inside the guest — just under the 3946 MiB Talos recommends for a control plane, which makes it log:

```text
task memorySizeCheck (1/1): NOTE: recommended memory size is 3946 MiB
task memorySizeCheck (1/1): NOTE: current total memory size is 3889 MiB
```

The node still runs, but budget about 5% above whatever Talos recommends for the role. `4608` for a control plane clears it comfortably; the `8192` default and larger worker sizes are unaffected.

### Recommended automatic-image configuration

```yaml
pool_id: "00000000-0000-0000-0000-000000000000"
sr_id: "11111111-1111-1111-1111-111111111111"
network_id: "22222222-2222-2222-2222-222222222222"
architecture: amd64
cores: 4
memory: 8192
disk_size: 32
```

### Manual template override

```yaml
pool_id: "00000000-0000-0000-0000-000000000000"
sr_id: "11111111-1111-1111-1111-111111111111"
network_id: "22222222-2222-2222-2222-222222222222"
template_id: "33333333-3333-3333-3333-333333333333"
architecture: amd64
cores: 4
memory: 8192
disk_size: 32
```

A manual template override bypasses automatic schematic-based image selection. The operator is responsible for ensuring that the template is compatible with the Talos version, Omni join workflow, architecture, and extensions requested by the cluster. Build one by hand following [Sidero's official Xen Orchestra guide](https://docs.siderolabs.com/talos/latest/platform-specific-installations/virtualized-platforms/xenorchestra).

## VM settings applied by the provider

The provider currently creates VMs with:

- A fast clone of the resolved template's disk, resized to `disk_size`
- A single VIF on `network_id`
- A `cidata` NoCloud config drive carrying the Omni Machine Join Config, attached before first boot
- `networkConfig` set to a minimal NoCloud network-config

Existing VMs are not resized or otherwise reconciled when Machine Class CPU, RAM, disk, or network settings change. Those settings apply to newly provisioned machines.

## Multiple provider instances

To manage more than one isolated Xen Orchestra/XCP-ng environment, run separate containers:

```yaml
services:
  xoa-site-a:
    image: ${PROVIDER_IMAGE}
    environment:
      OMNI_ENDPOINT: ${OMNI_ENDPOINT}
      OMNI_SERVICE_ACCOUNT_KEY: ${SITE_A_OMNI_KEY}
      XOA_ENDPOINT: wss://site-a.example.com
      XOA_TOKEN: ${SITE_A_XOA_TOKEN}
    command: ["--id", "xoa-site-a"]

  xoa-site-b:
    image: ${PROVIDER_IMAGE}
    environment:
      OMNI_ENDPOINT: ${OMNI_ENDPOINT}
      OMNI_SERVICE_ACCOUNT_KEY: ${SITE_B_OMNI_KEY}
      XOA_ENDPOINT: wss://site-b.example.com
      XOA_TOKEN: ${SITE_B_XOA_TOKEN}
    command: ["--id", "xoa-site-b"]
```

Create matching Omni infrastructure providers named `xoa-site-a` and `xoa-site-b`.
