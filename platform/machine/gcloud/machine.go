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

package gcloud

import (
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"

	"github.com/coreos/mantle/platform"
)

type machine struct {
	gc      *cluster
	name    string
	intIP   string
	extIP   string
	dir     string
	journal *platform.Journal
	console string
}

func (gm *machine) ID() string {
	return gm.name
}

func (gm *machine) IP() string {
	return gm.extIP
}

func (gm *machine) PrivateIP() string {
	return gm.intIP
}

func (gm *machine) SSHClient() (*ssh.Client, error) {
	return gm.gc.SSHClient(gm.IP())
}

func (gm *machine) PasswordSSHClient(user string, password string) (*ssh.Client, error) {
	return gm.gc.PasswordSSHClient(gm.IP(), user, password)
}

func (gm *machine) SSH(cmd string) ([]byte, error) {
	return gm.gc.SSH(gm, cmd)
}

func (gm *machine) SSHPipeOutput(cmd string, stdout io.Writer, stderr io.Writer) error {
	return gm.gc.SSHPipeOutput(gm, cmd, stdout, stderr)
}

func (m *machine) Reboot() error {
	return platform.RebootMachine(m, m.journal, m.gc.RuntimeConf())
}

func (gm *machine) Destroy() error {
	if err := gm.saveConsole(); err != nil {
		// log error, but do not fail to terminate instance
		plog.Error(err)
	}

	if err := gm.gc.api.TerminateInstance(gm.name); err != nil {
		return err
	}

	if gm.journal != nil {
		if err := gm.journal.Destroy(); err != nil {
			return err
		}
	}

	gm.gc.DelMach(gm)

	return nil
}

func (gm *machine) ConsoleOutput() string {
	return gm.console
}

func (gm *machine) saveConsole() error {
	var err error
	gm.console, err = gm.gc.api.GetConsoleOutput(gm.name)
	if err != nil {
		return err
	}

	path := filepath.Join(gm.dir, "console.txt")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	f.WriteString(gm.console)

	return nil
}
