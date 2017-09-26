// Copyright 2017 CoreOS, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package packet

import (
	"io"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/coreos/mantle/platform"
	"github.com/packethost/packngo"
)

type machine struct {
	cluster   *cluster
	device    *packngo.Device
	journal   *platform.Journal
	console   *console
	publicIP  string
	privateIP string
}

func (pm *machine) ID() string {
	return pm.device.ID
}

func (pm *machine) IP() string {
	return pm.publicIP
}

func (pm *machine) PrivateIP() string {
	return pm.privateIP
}

func (pm *machine) SSHClient() (*ssh.Client, error) {
	return pm.cluster.SSHClient(pm.IP())
}

func (pm *machine) PasswordSSHClient(user string, password string) (*ssh.Client, error) {
	return pm.cluster.PasswordSSHClient(pm.IP(), user, password)
}

func (pm *machine) SSH(cmd string) ([]byte, error) {
	return pm.cluster.SSH(pm, cmd)
}

func (pm *machine) SSHPipeOutput(cmd string, stdout io.Writer, stderr io.Writer) error {
	return pm.cluster.SSHPipeOutput(pm, cmd, stdout, stderr)
}

func (m *machine) Reboot() error {
	return platform.RebootMachine(m, m.journal, m.cluster.RuntimeConf())
}

func (pm *machine) Destroy() error {
	if err := pm.cluster.api.DeleteDevice(pm.ID()); err != nil {
		return err
	}

	if pm.journal != nil {
		if err := pm.journal.Destroy(); err != nil {
			return err
		}
	}

	pm.cluster.DelMach(pm)
	return nil
}

func (pm *machine) ConsoleOutput() string {
	if pm.console == nil {
		return ""
	}
	output := pm.console.Output()
	// The provisioning OS boots through iPXE and the real OS boots
	// through GRUB.  Try to ignore console logs from provisioning, but
	// it's better to return everything than nothing.
	grub := strings.Index(output, "GNU GRUB")
	if grub == -1 {
		plog.Warningf("Couldn't find GRUB banner in console output of %s", pm.ID())
		return output
	}
	linux := strings.Index(output[grub:], "Linux version")
	if linux == -1 {
		plog.Warningf("Couldn't find Linux banner in console output of %s", pm.ID())
		return output
	}
	return output[grub+linux:]
}
