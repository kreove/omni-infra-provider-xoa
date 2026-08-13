// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"testing"

	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider/data"
)

func TestApplyDefaults(t *testing.T) {
	t.Parallel()

	value := data.Data{}
	applyDefaults(&value)

	if value.Architecture != "amd64" {
		t.Fatalf("unexpected architecture %q", value.Architecture)
	}
}

func TestNoCloudMetaData(t *testing.T) {
	t.Parallel()

	got := noCloudMetaData("talos-worker-01")
	want := "instance-id: \"talos-worker-01\"\nlocal-hostname: \"talos-worker-01\"\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestValidateProviderDataAllowsAutomaticImage(t *testing.T) {
	t.Parallel()

	value := data.Data{
		PoolID:       "pool-1",
		SRID:         "sr-1",
		NetworkID:    "network-1",
		Architecture: "amd64",
		Cores:        2,
		Memory:       4096,
		DiskSize:     20,
	}

	if err := validateProviderData(value); err != nil {
		t.Fatalf("automatic image mode should be valid: %v", err)
	}
}

func TestValidateProviderDataAllowsManualTemplate(t *testing.T) {
	t.Parallel()

	value := data.Data{
		PoolID:       "pool-1",
		SRID:         "sr-1",
		NetworkID:    "network-1",
		TemplateID:   "template-1",
		Architecture: "amd64",
		Cores:        2,
		Memory:       4096,
		DiskSize:     20,
	}

	if err := validateProviderData(value); err != nil {
		t.Fatalf("manual template mode should be valid: %v", err)
	}
}

func TestValidateProviderDataRejectsMissingIDs(t *testing.T) {
	t.Parallel()

	base := data.Data{
		PoolID:       "pool-1",
		SRID:         "sr-1",
		NetworkID:    "network-1",
		Architecture: "amd64",
		Cores:        2,
		Memory:       4096,
		DiskSize:     20,
	}

	cases := []struct {
		name   string
		mutate func(*data.Data)
	}{
		{"missing pool_id", func(v *data.Data) { v.PoolID = "" }},
		{"missing sr_id", func(v *data.Data) { v.SRID = "" }},
		{"missing network_id", func(v *data.Data) { v.NetworkID = "" }},
		{"unsupported architecture", func(v *data.Data) { v.Architecture = "arm64" }},
		{"too few cores", func(v *data.Data) { v.Cores = 0 }},
		{"too little memory", func(v *data.Data) { v.Memory = 1024 }},
		{"too small disk", func(v *data.Data) { v.DiskSize = 1 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			value := base
			tc.mutate(&value)

			if err := validateProviderData(value); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}
