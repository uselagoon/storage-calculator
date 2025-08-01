/*
Copyright 2024.

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

package utils

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/onsi/ginkgo/v2"
)

func warnError(err error) {
	fmt.Fprintf(ginkgo.GinkgoWriter, "warning: %v\n", err)
}

var kubectlPath, kindPath string

func init() {
	if v, ok := os.LookupEnv("KIND_PATH"); ok {
		kindPath = v
	} else {
		kindPath = "kind"
	}
	if v, ok := os.LookupEnv("KUBECTL_PATH"); ok {
		kubectlPath = v
	} else {
		kubectlPath = "kubectl"
	}
	fmt.Println(kubectlPath, kindPath)
}

func Kubectl() string {
	return kubectlPath
}

// StartLocalServices starts local services
func StartLocalServices() error {
	cmd := exec.Command("docker", "compose", "up", "-d", "--build")
	_, err := Run(cmd)
	return err
}

// StopLocalServices stops local services
func StopLocalServices() {
	cmd := exec.Command("docker", "compose", "down")
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

// InstallBulkStorage installs the bulk storage class.
func InstallBulkStorage() error {
	cmd := exec.Command(kubectlPath, "apply", "-f", "test/e2e/testdata/bulk-storageclass.yaml")
	_, err := Run(cmd)
	return err
}

func StartMetricsConsumer() error {
	cmd := exec.Command(kubectlPath, "apply", "-f", "test/e2e/testdata/metrics-consumer.yaml")
	_, err := Run(cmd)
	return err
}

func StopMetricsConsumer() {
	cmd := exec.Command(kubectlPath, "delete", "-f", "test/e2e/testdata/metrics-consumer.yaml")
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

func RunCommonsCommand(ns, runCmd string) ([]byte, error) {
	cmd := exec.Command(kubectlPath, "-n", ns, "exec", "metrics-consumer", "--", "sh", "-c", runCmd)
	return Run(cmd)
}

// Run executes the provided command within this context
func Run(cmd *exec.Cmd) ([]byte, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir

	if err := os.Chdir(cmd.Dir); err != nil {
		fmt.Fprintf(ginkgo.GinkgoWriter, "chdir dir: %s\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	fmt.Fprintf(ginkgo.GinkgoWriter, "running: %s\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed with error: (%v) %s", command, err, string(output))
	}

	return output, nil
}

// LoadImageToKindCluster loads a local docker image to the kind cluster
func LoadImageToKindClusterWithName(name string) error {
	cluster := "storage-calculator"
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		cluster = v
	}
	kindOptions := []string{"load", "docker-image", name, "--name", cluster}
	cmd := exec.Command(kindPath, kindOptions...)
	_, err := Run(cmd)
	return err
}

// GetNonEmptyLines converts given command output string into individual objects
// according to line breakers, and ignores the empty elements in it.
func GetNonEmptyLines(output string) []string {
	var res []string
	elements := strings.Split(output, "\n")
	for _, element := range elements {
		if element != "" {
			res = append(res, element)
		}
	}

	return res
}

// GetProjectDir will return the directory where the project is
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, err
	}
	wd = strings.ReplaceAll(wd, "/test/e2e", "")
	return wd, nil
}

func CheckStringContainsStrings(str string, strs []string) error {
	for _, s := range strs {
		if !strings.Contains(str, s) {
			return fmt.Errorf("string %s not found in strings", s)
		}
	}
	return nil
}
