/*
Copyright 2021 k0s authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package install

import (
	"fmt"
	"github.com/k0sproject/k0s/pkg/component/worker"
	"github.com/k0sproject/k0s/pkg/container/runtime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/mount-utils"
)

func NewCleanUpConfig(dataDir string, criSocketPath string) (*CleanUpConfig, error) {
	runDir := "/run/k0s" // https://github.com/k0sproject/k0s/pull/591/commits/c3f932de85a0b209908ad39b817750efc4987395

	var ctrd *containerd
	var err error
	var runtimeType string

	if criSocketPath == "" {
		criSocketPath = fmt.Sprintf("unix:///%s/containerd.sock", runDir)
		ctrd = &containerd{
			binPath:    fmt.Sprintf("%s/%s", dataDir, "bin/containerd"),
			socketPath: fmt.Sprintf("%s/containerd.sock", runDir),
		}
		runtimeType = "cri"
	} else {
		runtimeType, criSocketPath, err = worker.SplitRuntimeConfig(criSocketPath)
		if err != nil {
			return nil, err
		}
	}

	return &CleanUpConfig{
		containerd:       ctrd,
		containerRuntime: runtime.NewContainerRuntime(runtimeType, criSocketPath),
		dataDir:          dataDir,
		runDir:           runDir,
	}, nil
}

func (c *CleanUpConfig) cleanupMount() error {
	var msg []string

	mounter := mount.New("")
	procMounts, err := mounter.List()
	if err != nil {
		return err
	}
	// search and unmount kubelet volume mounts
	for _, v := range procMounts {
		if strings.Contains(v.Path, "kubelet/pods") {
			if err = mounter.Unmount(v.Path); err != nil {
				msg = append(msg, err.Error())
			}
			if err := os.RemoveAll(v.Path); err != nil {
				msg = append(msg, err.Error())
			}
		}
	}
	if len(msg) > 0 {
		return fmt.Errorf("%v", strings.Join(msg, ", "))
	}
	return nil
}

func (c *CleanUpConfig) cleanupNetworkNamespace() error {
	var msg []string

	mounter := mount.New("")
	procMounts, err := mounter.List()
	if err != nil {
		return err
	}
	// search and unmount namespace mounts
	for _, v := range procMounts {
		if strings.Contains(v.Path, "run/netns") {
			if err = mounter.Unmount(v.Path); err != nil {
				msg = append(msg, err.Error())
			}
			if err := os.RemoveAll(v.Path); err != nil {
				msg = append(msg, err.Error())
			}
		}
	}
	if len(msg) > 0 {
		return fmt.Errorf("%v", strings.Join(msg, ", "))
	}
	return nil
}

func (c *CleanUpConfig) stopAllContainers() error {
	var msg []string

	containers, err := c.containerRuntime.ListContainers()
	if err != nil {
		return err
	}

	for _, container := range containers {
		logrus.Debugf("stopping container: %v", container)
		err := c.containerRuntime.StopContainer(container)
		if err != nil {
			if strings.Contains(err.Error(), "443: connect: connection refused") {
				// on a single node instance, we will see "connection refused" error. this is to be expected
				// since we're deleting the API pod itself. so we're ignoring this error
				logrus.Debugf("ignoring container stop err: %v", err.Error())
			} else {
				fmtError := fmt.Errorf("failed to stop running pod %v: err: %v", container, err)
				msg = append(msg, fmtError.Error())
			}
		}
	}
	if len(msg) > 0 {
		return fmt.Errorf("%v", strings.Join(msg, ", "))
	}
	return nil
}

func (c *CleanUpConfig) removeAllContainers() error {
	var msg []string

	containers, err := c.containerRuntime.ListContainers()
	if err != nil {
		return err
	}

	for _, container := range containers {
		err := c.containerRuntime.RemoveContainer(container)
		if err != nil {
			fmtError := fmt.Errorf("failed to remove pod %v: err: %v", container, err)
			msg = append(msg, fmtError.Error())
		}
	}
	if len(msg) > 0 {
		return fmt.Errorf("%v", strings.Join(msg, ", "))
	}
	return nil
}

func (c *CleanUpConfig) startContainerd() error {
	args := []string{
		fmt.Sprintf("--root=%s", filepath.Join(c.dataDir, "containerd")),
		fmt.Sprintf("--state=%s", filepath.Join(c.runDir, "containerd")),
		fmt.Sprintf("--address=%s", c.containerd.socketPath),
		"--config=/etc/k0s/containerd.toml",
	}
	cmd := exec.Command(c.containerd.binPath, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start containerd: %v", err)
	}

	c.containerd.cmd = cmd
	return nil
}

func (c *CleanUpConfig) stopContainerd() {
	logrus.Debug("attempting to stop containerd")
	logrus.Debugf("found containerd pid: %v", c.containerd.cmd.Process.Pid)
	if err := c.containerd.cmd.Process.Signal(os.Interrupt); err != nil {
		logrus.Errorf("failed to kill containerd: %v", err)
	}
	// if process, didn't exit, wait a few seconds and send SIGKILL
	if c.containerd.cmd.ProcessState.ExitCode() != -1 {
		time.Sleep(5 * time.Second)

		if err := c.containerd.cmd.Process.Kill(); err != nil {
			logrus.Errorf("failed to send SIGKILL to containerd: %v", err)
		}
	}
	logrus.Debug("successfully stopped containerd")
}

func (c *CleanUpConfig) RemoveAllDirectories() error {
	var msg []string

	// unmount any leftover overlays (such as in alpine)
	mounter := mount.New("")
	procMounts, err := mounter.List()
	if err != nil {
		return err
	}
	// search and unmount kubelet volume mounts
	for _, v := range procMounts {
		if strings.Compare(v.Path, fmt.Sprintf("%s/kubelet", c.dataDir)) == 0 {
			logrus.Debugf("%v is mounted! attempting to unmount...", v.Path)
			if err = mounter.Unmount(v.Path); err != nil {
				msg = append(msg, err.Error())
			}
		} else if strings.Compare(v.Path, c.dataDir) == 0 {
			logrus.Debugf("%v is mounted! attempting to unmount...", v.Path)
			if err = mounter.Unmount(v.Path); err != nil {
				msg = append(msg, err.Error())
			}
		}
	}

	removeCNILeftovers()

	logrus.Infof("deleting k0s generated data-dir (%v) and run-dir (%v)", c.dataDir, c.runDir)
	if err := os.RemoveAll(c.dataDir); err != nil {
		fmtError := fmt.Errorf("failed to delete %v. err: %v", c.dataDir, err)
		msg = append(msg, fmtError.Error())
	}
	if err := os.RemoveAll(c.runDir); err != nil {
		fmtError := fmt.Errorf("failed to delete %v. err: %v", c.runDir, err)
		msg = append(msg, fmtError.Error())
	}
	if len(msg) > 0 {
		return fmt.Errorf("%v", strings.Join(msg, ", "))
	}

	return nil
}

func removeCNILeftovers() {
	var msg []string

	calico10Conflist := "/etc/cni/net.d/10-calico.conflist"
	calicoKubeconfig := "/etc/cni/net.d/calico-kubeconfig"
	kuberouter10Conflist := "/etc/cni/net.d/10-kuberouter.conflist"

	if err := os.Remove(calico10Conflist); err != nil {
		msg = append(msg, fmt.Sprintf("failed to delete %v. err: %v", calico10Conflist, err))
	}
	if err := os.Remove(calicoKubeconfig); err != nil {
		msg = append(msg, fmt.Sprintf("failed to delete %v. err: %v", calicoKubeconfig, err))
	}
	if err := os.Remove(kuberouter10Conflist); err != nil {
		msg = append(msg, fmt.Sprintf("failed to delete %v. err: %v", kuberouter10Conflist, err))
	}

	if len(msg) > 0 {
		logrus.Debugf(strings.Join(msg, ", "))
	}
}
