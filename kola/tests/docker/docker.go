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

package docker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/context"

	"github.com/coreos/go-semver/semver"
	"github.com/coreos/mantle/kola/cluster"
	"github.com/coreos/mantle/kola/register"
	"github.com/coreos/mantle/lang/worker"
	"github.com/coreos/mantle/platform"
)

func init() {
	register.Register(&register.Test{
		Run:         dockerNetwork,
		ClusterSize: 2,
		Name:        "docker.network",
		UserData:    `#cloud-config`,
	})
	register.Register(&register.Test{
		Run:           dockerOldClient,
		ClusterSize:   1,
		Name:          "docker.oldclient",
		UserData:      `#cloud-config`,
		Architectures: []string{"amd64"}, // client is amd64 binary
	})
	register.Register(&register.Test{
		Run:                  dockerUserns,
		ClusterSize:          1,
		ExcludeArchitectures: []string{"arm64"}, // selinux
		Name:                 "docker.userns",
		// Source yaml:
		// https://github.com/coreos/container-linux-config-transpiler
		/*
			systemd:
			  units:
			  - name: docker.service
			    enable: true
			    dropins:
			      - name: 10-uesrns.conf
			        contents: |-
			          [Service]
			          Environment=DOCKER_OPTS=--userns-remap=dockremap
			storage:
			  files:
			  - filesystem: root
			    path: /etc/subuid
			    contents:
			      inline: "dockremap:100000:65536"
			  - filesystem: root
			    path: /etc/subgid
			    contents:
			      inline: "dockremap:100000:65536"
			passwd:
			  users:
			  - name: dockremap
			    create: {}
		*/
		UserData:   `{"ignition":{"version":"2.0.0","config":{}},"storage":{"files":[{"filesystem":"root","path":"/etc/subuid","contents":{"source":"data:,dockremap%3A100000%3A65536","verification":{}},"user":{},"group":{}},{"filesystem":"root","path":"/etc/subgid","contents":{"source":"data:,dockremap%3A100000%3A65536","verification":{}},"user":{},"group":{}}]},"systemd":{"units":[{"name":"docker.service","enable":true,"dropins":[{"name":"10-uesrns.conf","contents":"[Service]\nEnvironment=DOCKER_OPTS=--userns-remap=dockremap"}]}]},"networkd":{},"passwd":{"users":[{"name":"dockremap","create":{}}]}}`,
		MinVersion: semver.Version{Major: 1354}, // 1353 has kernel 4.9.x which is known to not work with userns on aws, see https://github.com/coreos/bugs/issues/1826
	})

	// This test covers all functionality that should be quick to run and can be
	// run:
	// 1. On an entirely default docker configuration on CL
	// 2. On a 'dirty machine' (in that other tests have already potentially run)
	//
	// Note, being able to run in parallel is desirable for these tests, but not
	// required. Parallelism should be tweaked at the subtest level in the
	// 'dockerBaseTests' implementation
	// The primary goal of using subtests here is to make things quicker to run.
	register.Register(&register.Test{
		Run:                  dockerBaseTests,
		ClusterSize:          1,
		Name:                 `docker.base`,
		ExcludeArchitectures: []string{"arm64"}, // selinux + crashes
		UserData:             `#cloud-config`,
	})

	register.Register(&register.Test{

		Run:                  func(c cluster.TestCluster) { testDockerInfo("btrfs", c) },
		ClusterSize:          1,
		ExcludeArchitectures: []string{"arm64"}, // selinux
		Name:                 "docker.btrfs-storage",
		// Note: copied verbatim from https://github.com/coreos/docs/blob/master/os/mounting-storage.md#creating-and-mounting-a-btrfs-volume-file after ct rendering
		UserData: `{
			"ignition": {
				"version": "2.0.0",
				"config": {}
			},
			"storage": {},
			"systemd": {
				"units": [
				{
					"name": "format-var-lib-docker.service",
					"enable": true,
					"contents": "[Unit]\nBefore=docker.service var-lib-docker.mount\nConditionPathExists=!/var/lib/docker.btrfs\n[Service]\nType=oneshot\nExecStart=/usr/bin/truncate --size=25G /var/lib/docker.btrfs\nExecStart=/usr/sbin/mkfs.btrfs /var/lib/docker.btrfs\n[Install]\nWantedBy=multi-user.target\n"
				},
				{
					"name": "var-lib-docker.mount",
					"enable": true,
					"contents": "[Unit]\nBefore=docker.service\nAfter=format-var-lib-docker.service\nRequires=format-var-lib-docker.service\n[Install]\nRequiredBy=docker.service\n[Mount]\nWhat=/var/lib/docker.btrfs\nWhere=/var/lib/docker\nType=btrfs\nOptions=loop,discard"
				}
				]
			},
			"networkd": {},
			"passwd": {}
		}`,
		// Roughly when the 'wrapper' script was removed so security + btrfs worked
		MinVersion: semver.Version{Major: 1400},
	})

	register.Register(&register.Test{
		// For a while we shipped /usr/lib/coreos/dockerd as the execstart of the
		// docker systemd unit.
		// This test verifies backwards compatibility with that unit to ensure
		// users who copied it into /etc aren't broken.
		Name:                 "docker.lib-coreos-dockerd-compat",
		ExcludeArchitectures: []string{"arm64"}, // selinux + dockerd crashes
		Run:                  dockerBaseTests,
		ClusterSize:          1,
		/* config-transpiler
		systemd:
		  units:
			- name: docker.service
		    contents: |-
		      [Unit]
		      Description=Docker Application Container Engine
		      Documentation=http://docs.docker.com
		      After=containerd.service docker.socket network.target
		      Requires=containerd.service docker.socket

		      [Service]
		      Type=notify
		      EnvironmentFile=-/run/flannel/flannel_docker_opts.env

		      # the default is not to use systemd for cgroups because the delegate issues still
		      # exists and systemd currently does not support the cgroup feature set required
		      # for containers run by docker
		      ExecStart=/usr/lib/coreos/dockerd --host=fd:// --containerd=/var/run/docker/libcontainerd/docker-containerd.sock $DOCKER_OPTS $DOCKER_CGROUPS $DOCKER_OPT_BIP $DOCKER_OPT_MTU $DOCKER_OPT_IPMASQ
		      ExecReload=/bin/kill -s HUP $MAINPID
		      LimitNOFILE=1048576
		      # Having non-zero Limit*s causes performance problems due to accounting overhead
		      # in the kernel. We recommend using cgroups to do container-local accounting.
		      LimitNPROC=infinity
		      LimitCORE=infinity
		      # Uncomment TasksMax if your systemd version supports it.
		      # Only systemd 226 and above support this version.
		      TasksMax=infinity
		      TimeoutStartSec=0
		      # set delegate yes so that systemd does not reset the cgroups of docker containers
		      Delegate=yes

		      [Install]
		      WantedBy=multi-user.target
		*/
		UserData: `{"ignition":{"version":"2.0.0","config":{}},"storage":{},"systemd":{"units":[{"name":"docker.service","contents":"[Unit]\nDescription=Docker Application Container Engine\nDocumentation=http://docs.docker.com\nAfter=containerd.service docker.socket network.target\nRequires=containerd.service docker.socket\n\n[Service]\nType=notify\nEnvironmentFile=-/run/flannel/flannel_docker_opts.env\n\n# the default is not to use systemd for cgroups because the delegate issues still\n# exists and systemd currently does not support the cgroup feature set required\n# for containers run by docker\nExecStart=/usr/lib/coreos/dockerd --host=fd:// --containerd=/var/run/docker/libcontainerd/docker-containerd.sock $DOCKER_OPTS $DOCKER_CGROUPS $DOCKER_OPT_BIP $DOCKER_OPT_MTU $DOCKER_OPT_IPMASQ\nExecReload=/bin/kill -s HUP $MAINPID\nLimitNOFILE=1048576\n# Having non-zero Limit*s causes performance problems due to accounting overhead\n# in the kernel. We recommend using cgroups to do container-local accounting.\nLimitNPROC=infinity\nLimitCORE=infinity\n# Uncomment TasksMax if your systemd version supports it.\n# Only systemd 226 and above support this version.\nTasksMax=infinity\nTimeoutStartSec=0\n# set delegate yes so that systemd does not reset the cgroups of docker containers\nDelegate=yes\n\n[Install]\nWantedBy=multi-user.target"}]},"networkd":{},"passwd":{}}`,
	})
}

// make a docker container out of binaries on the host
func genDockerContainer(m platform.Machine, name string, binnames []string) error {
	cmd := `tmpdir=$(mktemp -d); cd $tmpdir; echo -e "FROM scratch\nCOPY . /" > Dockerfile;
	        b=$(which %s); libs=$(sudo ldd $b | grep -o /lib'[^ ]*' | sort -u);
	        sudo rsync -av --relative --copy-links $b $libs ./;
	        sudo docker build -t %s .`

	if output, err := m.SSH(fmt.Sprintf(cmd, strings.Join(binnames, " "), name)); err != nil {
		return fmt.Errorf("failed to make %s container: output: %q status: %q", name, output, err)
	}

	return nil
}

func dockerBaseTests(c cluster.TestCluster) {
	c.Run("docker-info", func(c cluster.TestCluster) {
		testDockerInfo("overlay", c)
	})
	c.Run("resources", dockerResources)
	c.Run("networks-reliably", dockerNetworksReliably)
	c.Run("user-no-caps", dockerUserNoCaps)
}

// using a simple container, exercise various docker options that set resource
// limits. also acts as a regression test for
// https://github.com/coreos/bugs/issues/1246.
func dockerResources(c cluster.TestCluster) {
	m := c.Machines()[0]

	c.Log("creating sleep container")

	if err := genDockerContainer(m, "sleep", []string{"sleep"}); err != nil {
		c.Fatal(err)
	}

	dockerFmt := "docker run --rm %s sleep sleep 0.2"

	dCmd := func(arg string) string {
		return fmt.Sprintf(dockerFmt, arg)
	}

	ctx := context.Background()
	wg := worker.NewWorkerGroup(ctx, 10)

	// ref https://docs.docker.com/engine/reference/run/#runtime-constraints-on-resources
	for _, dockerCmd := range []string{
		// must set memory when setting memory-swap
		dCmd("--memory=10m --memory-swap=10m"),
		dCmd("--memory-reservation=10m"),
		dCmd("--kernel-memory=10m"),
		dCmd("--cpu-shares=100"),
		dCmd("--cpu-period=1000"),
		dCmd("--cpuset-cpus=0"),
		dCmd("--cpuset-mems=0"),
		dCmd("--cpu-quota=1000"),
		dCmd("--blkio-weight=10"),
		// none of these work in QEMU due to apparent lack of cfq for
		// blkio in virtual block devices.
		//dCmd("--blkio-weight-device=/dev/vda:10"),
		//dCmd("--device-read-bps=/dev/vda:1kb"),
		//dCmd("--device-write-bps=/dev/vda:1kb"),
		//dCmd("--device-read-iops=/dev/vda:10"),
		//dCmd("--device-write-iops=/dev/vda:10"),
		dCmd("--memory=10m --oom-kill-disable=true"),
		dCmd("--memory-swappiness=50"),
		dCmd("--shm-size=1m"),
	} {
		c.Logf("Executing %q", dockerCmd)

		// lol closures
		cmd := dockerCmd

		worker := func(c context.Context) error {
			// TODO: pass context thru to SSH
			output, err := m.SSH(cmd)
			if err != nil {
				return fmt.Errorf("failed to run %q: output: %q status: %q", dockerCmd, output, err)
			}
			return nil
		}

		if err := wg.Start(worker); err != nil {
			c.Fatal(wg.WaitError(err))
		}
	}

	if err := wg.Wait(); err != nil {
		c.Fatal(err)
	}
}

// Ensure that docker containers can make network connections outside of the host
func dockerNetwork(c cluster.TestCluster) {
	machines := c.Machines()
	src, dest := machines[0], machines[1]

	c.Log("creating ncat containers")

	if err := genDockerContainer(src, "ncat", []string{"ncat"}); err != nil {
		c.Fatal(err)
	}

	if err := genDockerContainer(dest, "ncat", []string{"ncat"}); err != nil {
		c.Fatal(err)
	}

	listener := func(c context.Context) error {
		// Will block until a message is recieved
		out, err := dest.SSH(
			`echo "HELLO FROM SERVER" | docker run -i -p 9988:9988 ncat ncat --idle-timeout 20 --listen 0.0.0.0 9988`,
		)
		if err != nil {
			return err
		}

		if !bytes.Equal(out, []byte("HELLO FROM CLIENT")) {
			return fmt.Errorf("unexpected result from listener: %q", out)
		}

		return nil
	}

	talker := func(c context.Context) error {
		// Wait until listener is ready before trying anything
		for {
			_, err := dest.SSH("sudo lsof -i TCP:9988 -s TCP:LISTEN | grep 9988 -q")
			if err == nil {
				break // socket is ready
			}

			exit, ok := err.(*ssh.ExitError)
			if !ok || exit.Waitmsg.ExitStatus() != 1 { // 1 is the expected exit of grep -q
				return err
			}

			select {
			case <-c.Done():
				return fmt.Errorf("timeout waiting for server")
			default:
				time.Sleep(100 * time.Millisecond)
			}
		}

		srcCmd := fmt.Sprintf(`echo "HELLO FROM CLIENT" | docker run -i ncat ncat %s 9988`, dest.PrivateIP())
		out, err := src.SSH(srcCmd)
		if err != nil {
			return err
		}

		if !bytes.Equal(out, []byte("HELLO FROM SERVER")) {
			return fmt.Errorf(`unexpected result from listener: "%v"`, out)
		}

		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := worker.Parallel(ctx, listener, talker); err != nil {
		c.Fatal(err)
	}
}

// Regression test for https://github.com/coreos/bugs/issues/1569 and
// https://github.com/coreos/docker/pull/31
func dockerOldClient(c cluster.TestCluster) {
	oldclient := "/usr/lib/kola/amd64/docker-1.9.1"
	if _, err := os.Stat(oldclient); err != nil {
		c.Skipf("Can't find old docker client to test: %v", err)
	}
	c.DropFile(oldclient)

	m := c.Machines()[0]

	if err := genDockerContainer(m, "echo", []string{"echo"}); err != nil {
		c.Fatal(err)
	}

	output, err := m.SSH("/home/core/docker-1.9.1 run echo echo 'IT WORKED'")
	if err != nil {
		c.Fatalf("failed to run old docker client: %q status: %q", output, err)
	}

	if !bytes.Equal(output, []byte("IT WORKED")) {
		c.Fatalf("unexpected result from docker client: %q", output)
	}
}

// Regression test for userns breakage under 1.12
func dockerUserns(c cluster.TestCluster) {
	m := c.Machines()[0]

	if err := genDockerContainer(m, "userns-test", []string{"echo", "sleep"}); err != nil {
		c.Fatal(err)
	}

	_, err := m.SSH(`sudo setenforce 1`)
	if err != nil {
		c.Fatalf("could not enable selinux")
	}
	output, err := m.SSH(`docker run userns-test echo fj.fj`)
	if err != nil {
		c.Fatalf("failed to run echo under userns: output: %q status: %q", output, err)
	}
	if !bytes.Equal(output, []byte("fj.fj")) {
		c.Fatalf("expected fj.fj, got %s", string(output))
	}

	// And just in case, verify that a container really is userns remapped
	_, err = m.SSH(`docker run -d --name=sleepy userns-test sleep 10000`)
	if err != nil {
		c.Fatalf("could not run sleep: %v", err)
	}
	uid_map, err := m.SSH(`until [[ "$(docker inspect -f {{.State.Running}} sleepy)" == "true" ]]; do sleep 0.1; done;
		pid=$(docker inspect -f {{.State.Pid}} sleepy);
		cat /proc/$pid/uid_map; docker kill sleepy &>/dev/null`)
	if err != nil {
		c.Fatalf("could not read uid mapping: %v", err)
	}
	// uid_map is of the form `$mappedNamespacePidStart   $realNamespacePidStart
	// $rangeLength`. We expect `0     100000      65536`
	mapParts := strings.Fields(strings.TrimSpace(string(uid_map)))
	if len(mapParts) != 3 {
		c.Fatalf("expected uid_map to have three parts, was: %s", string(uid_map))
	}
	if mapParts[0] != "0" && mapParts[1] != "100000" {
		c.Fatalf("unexpected userns mapping values: %v", string(uid_map))
	}
}

// Regression test for https://github.com/coreos/bugs/issues/1785
// Also, hopefully will catch any similar issues
func dockerNetworksReliably(c cluster.TestCluster) {
	m := c.Machines()[0]

	if err := genDockerContainer(m, "ping", []string{"sh", "ping"}); err != nil {
		c.Fatal(err)
	}

	output, err := m.SSH(`seq 1 100 | xargs -i -n 1 -P 20 docker run ping sh -c 'out=$(ping -c 1 172.17.0.1 -w 1); if [[ "$?" != 0 ]]; then echo "{} FAIL"; echo "$out"; exit 1; else echo "{} PASS"; fi'`)
	if err != nil {
		c.Fatalf("could not run 100 containers pinging the bridge: %v: %q", err, string(output))
	}
}

// Regression test for CVE-2016-8867
// CVE-2016-8867 gave a container capabilities, including fowner, even if it
// was a non-root user.
// We test that a user inside a container does not have any effective nor
// permitted capabilities (which is what the cve was).
// For good measure, we also check that fs permissions deny that user from
// accessing /root.
func dockerUserNoCaps(c cluster.TestCluster) {
	m := c.Machines()[0]

	if err := genDockerContainer(m, "captest", []string{"capsh", "sh", "grep", "cat", "ls"}); err != nil {
		c.Fatal(err)
	}

	output, err := m.SSH(`docker run --user 1000:1000 \
		-v /root:/root \
		captest sh -c \
		'cat /proc/self/status | grep -E "Cap(Eff|Prm)"; ls /root &>/dev/null && echo "FAIL: could read root" || echo "PASS: err reading root"'`)
	if err != nil {
		c.Fatalf("could not run container (we weren't even testing for that): %v: %q", err, string(output))
	}

	outputlines := strings.Split(string(output), "\n")
	if len(outputlines) < 3 {
		c.Fatalf("expected two lines of caps and an an error/succcess line. Got %q", string(output))
	}
	cap1, cap2 := strings.Fields(outputlines[0]), strings.Fields(outputlines[1])
	// The format of capabilities in /proc/*/status is e.g.: CapPrm:\t0000000000000000
	// We could parse the hex to its actual capabilities, but since we're looking for none, just checking it's all 0 is good enough.
	if len(cap1) != 2 || len(cap2) != 2 {
		c.Fatalf("capability lines didn't have two parts: %q", string(output))
	}
	if cap1[1] != "0000000000000000" || cap2[1] != "0000000000000000" {
		c.Fatalf("Permitted / effective capabilities were non-zero: %q", string(output))
	}

	// Finally, check for fail/success on reading /root
	if !strings.HasPrefix(outputlines[len(outputlines)-1], "PASS: ") {
		c.Fatalf("reading /root test failed: %q", string(output))
	}
}

// testDockerInfo test that docker info's output is as expected.  the expected
// filesystem may be asserted as one of 'overlay', 'btrfs', 'devicemapper'
// depending on how the machine was launched.
func testDockerInfo(expectedFs string, c cluster.TestCluster) {
	m := c.Machines()[0]

	dockerInfoJson, err := m.SSH(`curl -s --unix-socket /var/run/docker.sock http://docker/v1.24/info`)
	if err != nil {
		c.Fatalf("could not get dockerinfo: %v", err)
	}

	type simplifiedDockerInfo struct {
		ServerVersion string
		Driver        string
		CgroupDriver  string
		Runtimes      map[string]struct {
			Path string `json:"path"`
		}
		ContainerdCommit struct {
			ID       string
			Expected string
		}
		RuncCommit struct {
			ID       string
			Expected string
		}
		SecurityOptions []string
	}

	info := &simplifiedDockerInfo{}
	err = json.Unmarshal(dockerInfoJson, &info)
	if err != nil {
		c.Fatalf("could not unmarshal dockerInfo %q into known json: %v", string(dockerInfoJson), err)
	}

	// Canonicalize info
	sort.Strings(info.SecurityOptions)

	// Because we prefer overlay2/overlay for different docker versions, figure
	// out the correct driver to be testing for based on our docker version.
	expectedOverlayDriver := "overlay2"
	if strings.HasPrefix(info.ServerVersion, "1.12.") || strings.HasPrefix(info.ServerVersion, "17.04.") {
		expectedOverlayDriver = "overlay"
	}

	expectedFsDriverMap := map[string]string{
		"overlay":      expectedOverlayDriver,
		"btrfs":        "btrfs",
		"devicemapper": "devicemapper",
	}

	expectedFsDriver := expectedFsDriverMap[expectedFs]
	if info.Driver != expectedFsDriver {
		c.Errorf("unexpected driver: %v != %v", expectedFsDriver, info.Driver)
	}

	// Validations shared by all versions currently
	if !reflect.DeepEqual(info.SecurityOptions, []string{"seccomp", "selinux"}) {
		c.Errorf("unexpected security options: %+v", info.SecurityOptions)
	}

	if info.CgroupDriver != "cgroupfs" {
		c.Errorf("unexpected cgroup driver %v", info.CgroupDriver)
	}

	if info.ContainerdCommit.ID != info.ContainerdCommit.Expected {
		c.Errorf("commit mismatch for containerd: %v != %v", info.ContainerdCommit.Expected, info.ContainerdCommit.ID)
	}

	if info.RuncCommit.ID != info.RuncCommit.Expected {
		c.Errorf("commit mismatch for runc: %v != %v", info.RuncCommit.Expected, info.RuncCommit.ID)
	}

	if runcInfo, ok := info.Runtimes["runc"]; ok {
		if runcInfo.Path == "" {
			c.Errorf("expected non-empty runc path")
		}
	} else {
		c.Errorf("runc was not in runtimes: %+v", info.Runtimes)
	}
}
