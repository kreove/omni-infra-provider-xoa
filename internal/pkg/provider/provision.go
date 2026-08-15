// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package provider implements the Xen Orchestra Omni infrastructure provider.
package provider

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/siderolabs/omni/client/pkg/infra/provision"
	"github.com/siderolabs/omni/client/pkg/omni/resources/infra"
	xoaclient "github.com/vatesfr/xenorchestra-go-sdk/client"
	"go.uber.org/zap"

	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider/data"
	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider/resources"
)

const (
	bootDiskName = "disk0"

	// configDriveName identifies the NoCloud drive this provider builds and
	// attaches itself, so it can be recognized on reconcile.
	configDriveName = "cidata"
)

// Provisioner provisions Talos VMs on XCP-ng through Xen Orchestra.
type Provisioner struct {
	client              *xoClient
	imageFactoryBaseURL string
	imageBuilds         sync.Map
}

// NewProvisioner creates an XOA provisioner. connect must open a new Xen
// Orchestra connection each time it is called: the provider re-dials whenever
// the JSON-RPC WebSocket dies, which otherwise breaks it until restarted.
func NewProvisioner(connect func() (*xoaclient.Client, error), imageFactoryBaseURL string) (*Provisioner, error) {
	client, err := newXOClient(connect)
	if err != nil {
		return nil, err
	}

	return &Provisioner{
		client:              client,
		imageFactoryBaseURL: imageFactoryBaseURL,
	}, nil
}

// ProvisionSteps implements infra.Provisioner.
func (p *Provisioner) ProvisionSteps() []provision.Step[*resources.Machine] {
	return []provision.Step[*resources.Machine]{
		provision.NewStep("validateRequest", func(_ context.Context, _ *zap.Logger, pctx provision.Context[*resources.Machine]) error {
			if len(pctx.GetRequestID()) > 63 {
				return fmt.Errorf("machine request name cannot be longer than 63 characters")
			}

			var providerData data.Data
			if err := pctx.UnmarshalProviderData(&providerData); err != nil {
				return err
			}

			applyDefaults(&providerData)

			return validateProviderData(providerData)
		}),
		provision.NewStep("createSchematic", func(ctx context.Context, logger *zap.Logger, pctx provision.Context[*resources.Machine]) error {
			// Keep the serial console for hosts that provide one, but make tty0
			// the last console= so it owns /dev/console. XCP-ng HVM guests do
			// not get a serial port unless one is configured, and the VergeOS
			// provider this was ported from enabled one explicitly. Without
			// tty0 every message after early boot -- including kernel panics --
			// is written to a device that does not exist, leaving the XO
			// console blank and the failure invisible.
			schematic, err := pctx.GenerateSchematicID(
				ctx,
				logger,
				provision.WithExtraKernelArgs("console=ttyS0,38400n8", "console=tty0"),
				provision.WithoutConnectionParams(),
			)
			if err != nil {
				return err
			}

			pctx.State.TypedSpec().Value.Schematic = schematic
			pctx.State.TypedSpec().Value.TalosVersion = pctx.GetTalosVersion()

			return nil
		}),
		provision.NewStep("ensureTarget", func(ctx context.Context, _ *zap.Logger, pctx provision.Context[*resources.Machine]) error {
			var providerData data.Data
			if err := pctx.UnmarshalProviderData(&providerData); err != nil {
				return err
			}

			applyDefaults(&providerData)

			if _, err := p.client.GetPools(xoaclient.Pool{Id: providerData.PoolID}); err != nil {
				return fmt.Errorf("failed to resolve XO pool %q: %w", providerData.PoolID, err)
			}

			if _, err := p.client.GetStorageRepositoryById(providerData.SRID); err != nil {
				return fmt.Errorf("failed to resolve XO storage repository %q: %w", providerData.SRID, err)
			}

			if _, err := p.client.GetNetwork(xoaclient.Network{Id: providerData.NetworkID}); err != nil {
				return fmt.Errorf("failed to resolve XO network %q: %w", providerData.NetworkID, err)
			}

			return nil
		}),
		provision.NewStep("ensureImage", func(ctx context.Context, logger *zap.Logger, pctx provision.Context[*resources.Machine]) error {
			var providerData data.Data
			if err := pctx.UnmarshalProviderData(&providerData); err != nil {
				return err
			}

			applyDefaults(&providerData)

			templateID, ready, err := p.ensureTalosImage(ctx, logger, pctx, providerData)
			if err != nil {
				return err
			}

			if !ready {
				return provision.NewRetryInterval(15 * time.Second)
			}

			pctx.State.TypedSpec().Value.TemplateId = templateID

			return nil
		}),
		provision.NewStep("syncMachine", func(ctx context.Context, logger *zap.Logger, pctx provision.Context[*resources.Machine]) error {
			var providerData data.Data
			if err := pctx.UnmarshalProviderData(&providerData); err != nil {
				return err
			}

			applyDefaults(&providerData)

			vm, err := p.findVM(pctx.GetRequestID())
			if err != nil {
				return err
			}

			if vm == nil {
				createdVM, createErr := p.createVM(
					pctx.GetRequestID(),
					providerData,
					pctx.State.TypedSpec().Value.TemplateId,
				)
				if createErr != nil {
					return createErr
				}

				pctx.State.TypedSpec().Value.Uuid = createdVM.Id

				logger.Info(
					"created XO VM",
					zap.String("name", createdVM.NameLabel),
					zap.String("id", createdVM.Id),
				)

				return provision.NewRetryInterval(5 * time.Second)
			}

			if pctx.State.TypedSpec().Value.Uuid == "" {
				pctx.State.TypedSpec().Value.Uuid = vm.Id
			}

			// Must happen before the first power-on: Talos reads the config
			// drive during early boot and drops to maintenance mode if it is
			// not there.
			attached, err := p.ensureConfigDrive(vm, pctx.ConnectionParams.JoinConfig, providerData)
			if err != nil {
				return err
			}

			if attached {
				logger.Info(
					"attached NoCloud config drive",
					zap.String("name", vm.NameLabel),
				)

				return provision.NewRetryInterval(5 * time.Second)
			}

			if vm.PowerState != xoaclient.RunningPowerState {
				if err = p.client.StartVm(vm.Id); err != nil {
					return fmt.Errorf("failed to power on XO VM %q: %w", vm.NameLabel, err)
				}

				return provision.NewRetryInterval(5 * time.Second)
			}

			logger.Info(
				"machine is running",
				zap.String("name", vm.NameLabel),
				zap.String("id", vm.Id),
			)

			return nil
		}),
	}
}

func (p *Provisioner) createVM(
	name string,
	providerData data.Data,
	templateID string,
) (*xoaclient.Vm, error) {
	if templateID == "" {
		return nil, fmt.Errorf("cannot create VM %q without a resolved image template", name)
	}

	memoryBytes := int(providerData.Memory) * 1024 * 1024
	diskSizeBytes := int(providerData.DiskSize) * 1024 * 1024 * 1024

	vm, err := p.client.CreateVm(xoaclient.Vm{
		NameLabel:       name,
		NameDescription: "Talos machine managed by Sidero Omni",
		Template:        templateID,
		CPUs: xoaclient.CPUs{
			Number: providerData.Cores,
		},
		// Static AND dynamic memory must all be set to the requested size.
		// Setting only Static sends memoryStaticMax alone, and the VM inherits
		// dynamic-min/max (256 MiB) from the seed template. XAPI boots an HVM
		// guest whose dynamic memory is below its static max in Xen
		// populate-on-demand mode: the guest sees static-max of RAM but only
		// dynamic-min is actually backed, and the guest is expected to balloon
		// down before touching the rest. Talos runs no balloon driver in early
		// boot and touches memory immediately (init_on_alloc=1), so Xen killed
		// every VM within seconds -- before the kernel console existed -- with
		// "p2m_pod_demand_populate: out of PoD memory" visible only in the
		// host's `xl dmesg`. Dynamic[1] becomes memoryMax at creation and
		// Dynamic[0] becomes memoryMin in the SDK's follow-up vm.set, which
		// pins dynamic-min = dynamic-max = static-max and disables PoD, the
		// same thing Xen Orchestra's own VM-creation UI does.
		Memory: xoaclient.MemoryObject{
			Static:  []int{memoryBytes, memoryBytes},
			Dynamic: []int{memoryBytes, memoryBytes},
		},
		Disks: []xoaclient.Disk{
			{
				VDI: xoaclient.VDI{
					NameLabel: bootDiskName,
					SrId:      providerData.SRID,
					Size:      diskSizeBytes,
				},
			},
		},
		CloneType: xoaclient.CloneTypeFastClone,
		// Set explicitly rather than inheriting from the template: Talos will
		// not boot under BIOS, and a template built by an older version of this
		// provider (or supplied via template_id) may still be BIOS.
		Boot: xoaclient.Boot{Firmware: bootFirmware},
		VIFsMap: []map[string]string{
			{"network": providerData.NetworkID},
		},
		// CloudConfig/CloudNetworkConfig are deliberately not set. Passing them
		// makes Xen Orchestra build its own config drive, whose MBR-wrapped
		// layout Talos cannot detect (see cidata.go). ensureConfigDrive
		// attaches a drive Talos can actually read instead.
		Tags: []string{managedTag},
		// The SDK always sends xenStoreData in a follow-up vm.set call; a nil
		// Go map marshals to JSON null, which newer XO versions reject with
		// "must be object". An explicit empty map marshals to {} instead.
		XenstoreData: map[string]interface{}{},
		// CreateVm always waits for a target power state after creation; it
		// doesn't treat an empty PowerState as "don't wait". We create the VM
		// halted and power it on ourselves in a later syncMachine retry, the
		// same way the VergeOS provider did.
		PowerState: xoaclient.HaltedPowerState,
	}, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("failed to create XO VM %q: %w", name, err)
	}

	return vm, nil
}

// ensureConfigDrive attaches a NoCloud config drive carrying the Omni join
// config, building and uploading it if the VM does not have one yet. It
// reports whether a drive was attached on this call, so the caller can let the
// change settle before powering the machine on.
func (p *Provisioner) ensureConfigDrive(
	vm *xoaclient.Vm,
	joinConfig string,
	providerData data.Data,
) (bool, error) {
	disks, err := p.client.GetDisks(vm)
	if err != nil {
		return false, fmt.Errorf("failed to list disks on VM %q: %w", vm.NameLabel, err)
	}

	for _, disk := range disks {
		if disk.NameLabel == configDriveName {
			return false, nil
		}
	}

	if joinConfig == "" {
		return false, fmt.Errorf("Omni supplied an empty join config for VM %q", vm.NameLabel)
	}

	image, err := buildCidataImage(joinConfig, noCloudMetaData(vm.NameLabel), "version: 1\n")
	if err != nil {
		return false, fmt.Errorf("failed to build config drive for VM %q: %w", vm.NameLabel, err)
	}

	// CreateVDI uploads from a file, so the image has to be staged on disk.
	tmp, err := os.CreateTemp("", "omni-cidata-*.img")
	if err != nil {
		return false, fmt.Errorf("failed to stage config drive: %w", err)
	}

	defer os.Remove(tmp.Name())

	if _, err = tmp.Write(image); err != nil {
		tmp.Close()

		return false, fmt.Errorf("failed to write config drive image: %w", err)
	}

	if err = tmp.Close(); err != nil {
		return false, fmt.Errorf("failed to finalize config drive image: %w", err)
	}

	vdi, err := p.client.CreateVDI(xoaclient.CreateVDIReq{
		SRId:      providerData.SRID,
		Filepath:  tmp.Name(),
		NameLabel: configDriveName,
	})
	if err != nil {
		return false, fmt.Errorf("failed to upload config drive for VM %q: %w", vm.NameLabel, err)
	}

	var ok bool
	if err = p.client.Call("vm.attachDisk", map[string]interface{}{
		"vm":  vm.Id,
		"vdi": vdi.VDIId,
	}, &ok); err != nil {
		return false, fmt.Errorf("failed to attach config drive to VM %q: %w", vm.NameLabel, err)
	}

	return true, nil
}

func (p *Provisioner) findVM(name string) (*xoaclient.Vm, error) {
	vms, err := p.client.GetVms(xoaclient.Vm{NameLabel: name})
	if err != nil {
		if isNotFoundErr(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("failed to query XO VM %q: %w", name, err)
	}

	if len(vms) == 0 {
		return nil, nil
	}

	if len(vms) > 1 {
		return nil, fmt.Errorf("multiple XO VMs have the name %q", name)
	}

	return &vms[0], nil
}

// Deprovision implements infra.Provisioner.
func (p *Provisioner) Deprovision(
	_ context.Context,
	logger *zap.Logger,
	_ *resources.Machine,
	machineRequest *infra.MachineRequest,
) error {
	vm, err := p.findVM(machineRequest.Metadata().ID())
	if err != nil {
		return err
	}

	if vm == nil {
		logger.Info("machine deprovisioned")

		return nil
	}

	if vm.PowerState == xoaclient.RunningPowerState {
		// The SDK's HaltVm refuses to even attempt a stop until XO reports PV
		// drivers detected in the guest, with no fallback (see
		// https://github.com/vatesfr/terraform-provider-xenorchestra/issues/220).
		// A machine being deprovisioned may never have booted successfully
		// (bad join config, crashed Talos, missing guest-agent extension), so
		// deprovisioning can't depend on that. Force-stop directly instead.
		var stopped bool
		if err = p.client.Call("vm.stop", map[string]interface{}{"id": vm.Id, "force": true}, &stopped); err != nil {
			return fmt.Errorf("failed to power off XO VM %q: %w", vm.NameLabel, err)
		}

		return provision.NewRetryInterval(5 * time.Second)
	}

	vifs, err := p.client.GetVIFs(vm)
	if err != nil {
		return fmt.Errorf("failed to list VIFs while deleting VM %q: %w", vm.NameLabel, err)
	}

	for _, vif := range vifs {
		// The SDK's DeleteVIF unconditionally disconnects before deleting,
		// but by this point the VM is confirmed halted, so every VIF is
		// already unplugged; XO rejects disconnecting an unattached VIF.
		// Call vif.delete directly instead of going through DeleteVIF.
		var deleted bool
		if err = p.client.Call("vif.delete", map[string]interface{}{"id": vif.Id}, &deleted); err != nil {
			return fmt.Errorf("failed to delete VIF %q from VM %q: %w", vif.Id, vm.NameLabel, err)
		}
	}

	disks, err := p.client.GetDisks(vm)
	if err != nil {
		return fmt.Errorf("failed to list disks while deleting VM %q: %w", vm.NameLabel, err)
	}

	for _, disk := range disks {
		if err = p.client.DeleteDisk(*vm, disk); err != nil {
			return fmt.Errorf("failed to delete disk %q from VM %q: %w", disk.VDIId, vm.NameLabel, err)
		}
	}

	if err = p.client.DeleteVm(vm.Id); err != nil {
		return fmt.Errorf("failed to delete XO VM %q: %w", vm.NameLabel, err)
	}

	return provision.NewRetryInterval(5 * time.Second)
}
