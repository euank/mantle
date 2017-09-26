// Copyright 2016 CoreOS, Inc.
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

package qemu

import (
	"io"
	"io/ioutil"

	"golang.org/x/crypto/ssh"

	"github.com/coreos/mantle/platform"
	"github.com/coreos/mantle/platform/local"
	"github.com/coreos/mantle/system/exec"
)

type machine struct {
	qc          *Cluster
	id          string
	qemu        exec.Cmd
	netif       *local.Interface
	journal     *platform.Journal
	consolePath string
	console     string
}

func (m *machine) ID() string {
	return m.id
}

func (m *machine) IP() string {
	return m.netif.DHCPv4[0].IP.String()
}

func (m *machine) PrivateIP() string {
	return m.netif.DHCPv4[0].IP.String()
}

func (m *machine) SSHClient() (*ssh.Client, error) {
	return m.qc.SSHClient(m.IP())
}

func (m *machine) PasswordSSHClient(user string, password string) (*ssh.Client, error) {
	return m.qc.PasswordSSHClient(m.IP(), user, password)
}

func (m *machine) SSH(cmd string) ([]byte, error) {
	return m.qc.SSH(m, cmd)
}

func (m *machine) SSHPipeOutput(cmd string, stdout io.Writer, stderr io.Writer) error {
	return m.qc.SSHPipeOutput(m, cmd, stdout, stderr)
}

func (m *machine) Reboot() error {
	return platform.RebootMachine(m, m.journal, m.qc.RuntimeConf())
}

func (m *machine) Destroy() error {
	err := m.qemu.Kill()
	if err2 := m.journal.Destroy(); err == nil && err2 != nil {
		err = err2
	}

	buf, err2 := ioutil.ReadFile(m.consolePath)
	if err2 == nil {
		m.console = string(buf)
	} else if err == nil {
		err = err2
	}

	m.qc.DelMach(m)

	return err
}

func (m *machine) ConsoleOutput() string {
	return m.console
}
