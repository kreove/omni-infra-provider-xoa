// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package provider implements the Xen Orchestra Omni infrastructure provider.
package provider

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/siderolabs/omni/client/pkg/infra/provision"
	"github.com/siderolabs/omni/client/pkg/omni/resources/infra"
	xoaclient "github.com/vatesfr/xenorchestra-go-sdk/client"
	"go.uber.org/zap"

	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider/data"
	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider/resources"
)

const bootDiskName = "disk0"

// Provisioner provisions Talos VMs on XCP-ng through Xen Orchestra.
type Provisioner struct {
	client              *xoaclient.Client
	imageFactoryBaseURL string
	imageBuilds         sync.Map
}

// NewProvisioner creates an XOA provisioner.
func NewProvisioner(client *xoaclient.Client, imageFactoryBaseURL string) *Provisioner {
	return &Provisioner{
		client:              client,
		imageFactoryBaseURL: imageFactoryBaseURL,
	}
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
			schematic, err := pctx.GenerateSchematicID(
				ctx,
				logger,
				provision.WithExtraKernelArgs("console=ttyS0,38400n8"),
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
					pctx.ConnectionParams.JoinConfig,
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
	joinConfig string,
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
		Memory: xoaclient.MemoryObject{
			Static: []int{memoryBytes, memoryBytes},
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
		VIFsMap: []map[string]string{
			{"network": providerData.NetworkID},
		},
		CloudConfig:        joinConfig,
		CloudNetworkConfig: "version: 1\n",
		Tags:               []string{managedTag},
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
		// https://github.com/terra-farm/terraform-provider-xenorchestra/issues/220).
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
