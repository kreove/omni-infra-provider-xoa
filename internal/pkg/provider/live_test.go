// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cosi-project/runtime/pkg/controller"
	"github.com/siderolabs/omni/client/pkg/omni/resources/infra"
	xoaclient "github.com/vatesfr/xenorchestra-go-sdk/client"
	"go.uber.org/zap/zaptest"

	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider/data"
)

// TestLiveXOProvisioning exercises the real provisioning and deprovisioning
// code paths (golden-template build, VM clone, VIF/cloud-init, power on/off,
// deprovision) against a real Xen Orchestra instance. It's a manual,
// opt-in integration test, not part of the normal `go test ./...`/CI run:
// it only runs when XOA_LIVE_ENDPOINT is set, and it creates and deletes
// real infrastructure (a VDI, a seed VM, a template, and a cloned VM) in the
// pool/SR/network you point it at.
//
// Required env vars: XOA_LIVE_ENDPOINT, XOA_LIVE_USERNAME, XOA_LIVE_PASSWORD
// (or XOA_LIVE_TOKEN), XOA_LIVE_POOL_ID, XOA_LIVE_SR_ID, XOA_LIVE_NETWORK_ID.
// Optional: XOA_LIVE_SCHEMATIC, XOA_LIVE_TALOS_VERSION (defaults to a known
// good no-extensions schematic and a recent Talos release) and
// XOA_LIVE_KEEP_TEMPLATE=1 to skip deleting the built golden template
// afterward so it can be reused by a real Machine Request.
func TestLiveXOProvisioning(t *testing.T) {
	endpoint := os.Getenv("XOA_LIVE_ENDPOINT")
	if endpoint == "" {
		t.Skip("XOA_LIVE_ENDPOINT not set; skipping live Xen Orchestra integration test")
	}

	poolID := requireEnv(t, "XOA_LIVE_POOL_ID")
	srID := requireEnv(t, "XOA_LIVE_SR_ID")
	networkID := requireEnv(t, "XOA_LIVE_NETWORK_ID")

	cfg := xoaclient.Config{
		Url:                endpoint,
		Username:           os.Getenv("XOA_LIVE_USERNAME"),
		Password:           os.Getenv("XOA_LIVE_PASSWORD"),
		Token:              os.Getenv("XOA_LIVE_TOKEN"),
		InsecureSkipVerify: true,
	}

	iface, err := xoaclient.NewClient(cfg)
	if err != nil {
		t.Fatalf("failed to connect/authenticate to Xen Orchestra: %v", err)
	}

	client, ok := iface.(*xoaclient.Client)
	if !ok {
		t.Fatalf("unexpected client implementation type %T", iface)
	}

	p := NewProvisioner(client, "https://factory.talos.dev")

	schematic := envOrDefault("XOA_LIVE_SCHEMATIC", "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba")
	talosVersion := envOrDefault("XOA_LIVE_TALOS_VERSION", "v1.13.8")

	imageURL, cacheName, err := buildTalosImageReference(p.imageFactoryBaseURL, schematic, talosVersion, "amd64")
	if err != nil {
		t.Fatalf("buildTalosImageReference failed: %v", err)
	}

	t.Logf("building golden template from %s (cache name %s)", imageURL, cacheName)

	providerData := data.Data{
		PoolID:       poolID,
		SRID:         srID,
		NetworkID:    networkID,
		Architecture: "amd64",
		Cores:        2,
		Memory:       2048,
		DiskSize:     10,
	}

	buildCtx, buildCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer buildCancel()

	templateID, err := p.importGoldenTemplate(buildCtx, providerData, imageURL, cacheName)
	if err != nil {
		t.Fatalf("importGoldenTemplate failed: %v", err)
	}

	t.Logf("golden template built: %s", templateID)

	if os.Getenv("XOA_LIVE_KEEP_TEMPLATE") != "1" {
		t.Cleanup(func() {
			if err := client.DeleteVm(templateID); err != nil {
				t.Logf("cleanup: failed to delete golden template %s: %v", templateID, err)
			} else {
				t.Logf("cleanup: deleted golden template %s", templateID)
			}
		})
	} else {
		t.Logf("XOA_LIVE_KEEP_TEMPLATE=1: leaving template %s in place for reuse", templateID)
	}

	vmName := fmt.Sprintf("omni-xoa-live-test-%d", time.Now().Unix())
	joinConfig := "#cloud-config\n# live test placeholder join config, not a real Omni join token\n"

	vm, err := p.createVM(vmName, joinConfig, providerData, templateID)
	if err != nil {
		t.Fatalf("createVM failed: %v", err)
	}

	t.Logf("created VM %s (id=%s)", vm.NameLabel, vm.Id)

	t.Cleanup(func() {
		logger := zaptest.NewLogger(t)
		machineRequest := infra.NewMachineRequest(vmName)

		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			err := p.Deprovision(context.Background(), logger, nil, machineRequest)
			if err == nil {
				t.Logf("cleanup: deprovisioned VM %s", vmName)

				return
			}

			var requeue *controller.RequeueError
			if !errors.As(err, &requeue) {
				t.Logf("cleanup: Deprovision failed for VM %s: %v", vmName, err)

				return
			}

			time.Sleep(requeue.Interval())
		}

		t.Logf("cleanup: gave up deprovisioning VM %s before the deadline", vmName)
	})

	disks, err := client.GetDisks(vm)
	if err != nil {
		t.Fatalf("GetDisks failed: %v", err)
	}

	// XO attaches its own cloud-init config drive alongside the boot disk, so
	// there are normally two disks: "disk0" and "XO CloudConfigDrive".
	var bootDisk *xoaclient.Disk
	for i, d := range disks {
		if d.NameLabel == bootDiskName {
			bootDisk = &disks[i]
		}
	}

	if bootDisk == nil {
		t.Fatalf("boot disk %q not found among %d disks", bootDiskName, len(disks))
	}

	t.Logf("boot disk: %s (size=%d bytes, of %d total disks)", bootDisk.VDIId, bootDisk.Size, len(disks))

	vifs, err := client.GetVIFs(vm)
	if err != nil {
		t.Fatalf("GetVIFs failed: %v", err)
	}

	if len(vifs) != 1 {
		t.Fatalf("expected exactly one VIF, got %d", len(vifs))
	}

	if vifs[0].Network != networkID {
		t.Fatalf("VIF attached to network %q, want %q", vifs[0].Network, networkID)
	}

	t.Logf("VIF: %s on network %s", vifs[0].Id, vifs[0].Network)

	if err = client.StartVm(vm.Id); err != nil {
		t.Fatalf("StartVm failed: %v", err)
	}

	running, err := client.GetVm(xoaclient.Vm{Id: vm.Id})
	if err != nil {
		t.Fatalf("GetVm failed: %v", err)
	}

	if running.PowerState != xoaclient.RunningPowerState {
		t.Fatalf("expected VM to be running, got power state %q", running.PowerState)
	}

	t.Logf("VM is running; cleanup will power off and deprovision it")
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()

	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s must be set for the live Xen Orchestra integration test", name)
	}

	return value
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}

	return fallback
}
