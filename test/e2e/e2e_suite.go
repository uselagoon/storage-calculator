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

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/uselagoon/storage-calculator/test/utils"
)

const (
	namespace = "storage-calculator-system"
	timeout   = "600s"
)

var (
	duration = 600 * time.Second
	interval = 1 * time.Second

	metricLabels = []string{
		"lagoon_storage_calculator_kilobytes",
	}
)

var _ = Describe("controller", Ordered, func() {
	BeforeAll(func() {
		By("start local services")
		Expect(utils.StartLocalServices()).To(Succeed())

		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, _ = utils.Run(cmd)

		// when running a re-test, it is best to make sure the old namespace doesn't exist
		By("removing existing test resources")

		// remove the example namespace
		cmd = exec.Command("kubectl", "delete", "ns", "example-project-main")
		_, _ = utils.Run(cmd)
	})

	// comment to prevent cleaning up controller namespace and local services
	AfterAll(func() {
		By("stop metrics consumer")
		utils.StopMetricsConsumer()

		// remove the example namespace
		cmd := exec.Command("kubectl", "delete", "ns", "example-project-main")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)

		By("stop local services")
		utils.StopLocalServices()
	})

	Context("Operator", func() {
		It("should run successfully", func() {
			// start tests
			var controllerPodName string
			var err error

			// projectimage stores the name of the image used in the example
			var projectimage = "example.com/storage-calculator:v0.0.1"

			By("building the manager(Operator) image")
			cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectimage))
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("loading the the manager(Operator) image on Kind")
			err = utils.LoadImageToKindClusterWithName(projectimage)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("deploying the controller-manager")
			cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectimage))
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func() error {
				// Get pod name

				cmd = exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
				podNames := utils.GetNonEmptyLines(string(podOutput))
				if len(podNames) != 1 {
					return fmt.Errorf("expect 1 controller pods running, but got %d", len(podNames))
				}
				controllerPodName = podNames[0]
				ExpectWithOffset(2, controllerPodName).Should(ContainSubstring("controller-manager"))

				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				status, err := utils.Run(cmd)
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
				if string(status) != "Running" {
					return fmt.Errorf("controller pod in %s status", status)
				}
				return nil
			}
			EventuallyWithOffset(1, verifyControllerUp, time.Minute, time.Second).Should(Succeed())

			By("start metrics consumer")
			Expect(utils.StartMetricsConsumer()).To(Succeed())

			time.Sleep(30 * time.Second)

			By("creating a basic deployment")
			cmd = exec.Command(
				"kubectl",
				"apply",
				"-f",
				"test/e2e/testdata/example-env.yaml",
			)
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("wait for storage calculator to run")
			verifyStorageCalculatorRuns := func() error {
				cmd = exec.Command("kubectl", "logs",
					controllerPodName, "-c", "manager",
					"-n", namespace,
				)
				podlogs, err := utils.Run(cmd)
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
				if !strings.Contains(string(podlogs), "volumes from storage-calculator pod example-project-main") {
					return fmt.Errorf("storage-calculator-pod not created")
				}
				return nil
			}
			EventuallyWithOffset(1, verifyStorageCalculatorRuns, duration, interval).Should(Succeed())

			By("validating that unauthenticated metrics requests fail")
			runCmd := `curl -s -k https://storage-calculator-controller-manager-metrics-service.storage-calculator-system.svc.cluster.local:8443/metrics | grep -v "#" | grep "lagoon_"`
			_, err = utils.RunCommonsCommand(namespace, runCmd)
			ExpectWithOffset(2, err).To(HaveOccurred())

			By("validating that authenticated metrics requests succeed with metrics")
			runCmd = `curl -s -k -H "Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)" https://storage-calculator-controller-manager-metrics-service.storage-calculator-system.svc.cluster.local:8443/metrics | grep -v "#" | grep "lagoon_"`
			output, err := utils.RunCommonsCommand(namespace, runCmd)
			ExpectWithOffset(2, err).NotTo(HaveOccurred())
			fmt.Printf("metrics: %s", string(output))
			err = utils.CheckStringContainsStrings(string(output), metricLabels)
			ExpectWithOffset(2, err).NotTo(HaveOccurred())
			// End tests
		})
	})
})
