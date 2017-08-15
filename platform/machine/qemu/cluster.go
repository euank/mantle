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
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/coreos/pkg/capnslog"
	"github.com/satori/go.uuid"

	"github.com/coreos/mantle/platform"
	"github.com/coreos/mantle/platform/conf"
	"github.com/coreos/mantle/platform/local"
	"github.com/coreos/mantle/system/exec"
	"github.com/coreos/mantle/system/ns"
)

// Options contains QEMU-specific options for the cluster.
type Options struct {
	// DiskImage is the full path to the disk image to boot in QEMU.
	DiskImage string
	Board     string

	// BIOSImage is name of the BIOS file to pass to QEMU.
	// It can be a plain name, or a full path.
	BIOSImage string

	*platform.Options
}

// Cluster is a local cluster of QEMU-based virtual machines.
//
// XXX: must be exported so that certain QEMU tests can access struct members
// through type assertions.
type Cluster struct {
	opts *Options

	mu sync.Mutex
	*local.LocalCluster
}

var (
	plog = capnslog.NewPackageLogger("github.com/coreos/mantle", "kola/platform/machine/qemu")
)

// NewCluster creates a Cluster instance, suitable for running virtual
// machines in QEMU.
func NewCluster(opts *Options, rconf *platform.RuntimeConfig) (platform.Cluster, error) {
	lc, err := local.NewLocalCluster(opts.BaseName, rconf)
	if err != nil {
		return nil, err
	}

	qc := &Cluster{
		opts:         opts,
		LocalCluster: lc,
	}

	return qc, nil
}

func (qc *Cluster) NewMachine(userdata *conf.UserData) (platform.Machine, error) {
	id := uuid.NewV4()

	dir := filepath.Join(qc.RuntimeConf().OutputDir, id.String())
	if err := os.Mkdir(dir, 0777); err != nil {
		return nil, err
	}

	// hacky solution for cloud config ip substitution
	// NOTE: escaping is not supported
	qc.mu.Lock()
	netif := qc.Dnsmasq.GetInterface("br0")
	ip := strings.Split(netif.DHCPv4[0].String(), "/")[0]

	conf, err := qc.RenderUserData(userdata, map[string]string{
		"$public_ipv4":  ip,
		"$private_ipv4": ip,
	})
	if err != nil {
		qc.mu.Unlock()
		return nil, err
	}
	qc.mu.Unlock()

	var confPath string
	if conf.IsIgnition() {
		confPath = filepath.Join(dir, "ignition.json")
		if err := conf.WriteFile(confPath); err != nil {
			return nil, err
		}
	} else {
		confPath, err = local.MakeConfigDrive(conf, dir)
		if err != nil {
			return nil, err
		}
	}

	journal, err := platform.NewJournal(dir)
	if err != nil {
		return nil, err
	}

	qm := &machine{
		qc:          qc,
		id:          id.String(),
		netif:       netif,
		journal:     journal,
		consolePath: filepath.Join(dir, "console.txt"),
	}

	var qmCmd []string
	switch qc.opts.Board {
	case "amd64-usr":
		qmCmd = []string{
			"qemu-system-x86_64",
			"-machine", "accel=kvm",
			"-cpu", "host",
		}
	case "arm64-usr":
		qmCmd = []string{
			"qemu-system-aarch64",
			"-machine", "virt",
			"-cpu", "cortex-a57",
		}
	default:
		panic(qc.opts.Board)
	}

	qmMac := qm.netif.HardwareAddr.String()
	qmCmd = append(qmCmd,
		"-bios", qc.opts.BIOSImage,
		"-smp", "1",
		"-m", "1024",
		"-uuid", qm.id,
		"-display", "none",
		"-add-fd", "fd=4,set=1",
		"-drive", "if=none,id=blk,format=qcow2,file=/dev/fdset/1",
		"-device", qc.virtio("blk", "drive=blk"),
		"-netdev", "tap,id=tap,fd=3",
		"-device", qc.virtio("net", "netdev=tap,mac="+qmMac),
		"-chardev", "file,id=log,path="+qm.consolePath,
		"-serial", "chardev:log",
	)

	if conf.IsIgnition() {
		qmCmd = append(qmCmd,
			"-fw_cfg", "name=opt/com.coreos/config,file="+confPath)
	} else {
		qmCmd = append(qmCmd,
			"-fsdev", "local,id=cfg,security_model=none,readonly,path="+confPath,
			"-device", qc.virtio("9p", "fsdev=cfg,mount_tag=config-2"))
	}

	diskFile, err := setupDisk(qc.opts.DiskImage)
	if err != nil {
		return nil, err
	}
	defer diskFile.Close()

	qc.mu.Lock()

	tap, err := qc.NewTap("br0")
	if err != nil {
		qc.mu.Unlock()
		return nil, err
	}
	defer tap.Close()

	plog.Debugf("NewMachine: %q", qmCmd)

	qm.qemu = qm.qc.NewCommand(qmCmd[0], qmCmd[1:]...)

	qc.mu.Unlock()

	cmd := qm.qemu.(*ns.Cmd)
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = append(cmd.ExtraFiles,
		tap.File, // fd=3
		diskFile, // fd=4
	)

	if err = qm.qemu.Start(); err != nil {
		return nil, err
	}

	if err := platform.StartMachine(qm, qm.journal, qc.RuntimeConf()); err != nil {
		qm.Destroy()
		return nil, err
	}

	qc.AddMach(qm)

	return qm, nil
}

// The virtio device name differs between machine types but otherwise
// configuration is the same. Use this to help construct device args.
func (qc *Cluster) virtio(device, args string) string {
	var suffix string
	switch qc.opts.Board {
	case "amd64-usr":
		suffix = "pci"
	case "arm64-usr":
		suffix = "device"
	default:
		panic(qc.opts.Board)
	}
	return fmt.Sprintf("virtio-%s-%s,%s", device, suffix, args)
}

// Create a nameless temporary qcow2 image file backed by a raw image.
func setupDisk(imageFile string) (*os.File, error) {
	// a relative path would be interpreted relative to /tmp
	backingFile, err := filepath.Abs(imageFile)
	if err != nil {
		return nil, err
	}
	// keep the COW image from breaking if the "latest" symlink changes
	backingFile, err = filepath.EvalSymlinks(backingFile)
	if err != nil {
		return nil, err
	}

	dstFile, err := ioutil.TempFile("", "mantle-qemu")
	if err != nil {
		return nil, err
	}
	dstFileName := dstFile.Name()
	defer os.Remove(dstFileName)
	dstFile.Close()

	qcowOpts := fmt.Sprintf("backing_file=%s,backing_fmt=raw,lazy_refcounts=on", backingFile)
	qemuImg := exec.Command("qemu-img", "create", "-f", "qcow2",
		"-o", qcowOpts, dstFileName)
	qemuImg.Stderr = os.Stderr

	if err := qemuImg.Run(); err != nil {
		return nil, err
	}

	return os.OpenFile(dstFileName, os.O_RDWR, 0)
}
