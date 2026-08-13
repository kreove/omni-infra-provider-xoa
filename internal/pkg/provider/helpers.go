// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"fmt"
	"math"
	"strings"

	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider/data"
)

func validateProviderData(value data.Data) error {
	if strings.TrimSpace(value.PoolID) == "" {
		return fmt.Errorf("pool_id must not be empty")
	}

	if strings.TrimSpace(value.SRID) == "" {
		return fmt.Errorf("sr_id must not be empty")
	}

	if strings.TrimSpace(value.NetworkID) == "" {
		return fmt.Errorf("network_id must not be empty")
	}

	if value.Architecture != "amd64" {
		return fmt.Errorf("architecture %q is not supported by this alpha provider; use amd64", value.Architecture)
	}

	if value.Cores < 1 {
		return fmt.Errorf("cores must be greater than zero")
	}

	if value.Memory < 2048 {
		return fmt.Errorf("memory must be at least 2048 MiB")
	}

	if value.Memory > math.MaxInt {
		return fmt.Errorf("memory value is too large")
	}

	if value.DiskSize < 5 {
		return fmt.Errorf("disk_size must be at least 5 GiB")
	}

	return nil
}

func applyDefaults(value *data.Data) {
	if value.Architecture == "" {
		value.Architecture = "amd64"
	}
}

func noCloudMetaData(name string) string {
	return fmt.Sprintf("instance-id: %q\nlocal-hostname: %q\n", name, name)
}
