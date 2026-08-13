// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package data defines XOA MachineClass configuration.
package data

import _ "embed"

// Schema is reported to Omni for MachineClass rendering and validation.
//
//go:embed schema.json
var Schema []byte

// Data and schema.json must remain in sync.
type Data struct {
	PoolID       string `yaml:"pool_id"`
	SRID         string `yaml:"sr_id"`
	NetworkID    string `yaml:"network_id"`
	TemplateID   string `yaml:"template_id,omitempty"`
	Architecture string `yaml:"architecture"`
	Cores        int    `yaml:"cores"`
	Memory       uint64 `yaml:"memory"`
	DiskSize     int64  `yaml:"disk_size"`
}
