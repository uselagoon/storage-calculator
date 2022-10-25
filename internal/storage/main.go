package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/uselagoon/storage-calculator/internal/broker"

	ns "github.com/uselagoon/machinery/utils/namespace"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=list;get;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=list;get;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=list;get
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=list;get;watch

// Calculator handles collecting storage calculator information
type Calculator struct {
	Client          client.Client
	MQ              *broker.MQ
	Log             logr.Logger
	Scheme          *runtime.Scheme
	IgnoreRegex     string
	CalculatorImage string
	Debug           bool
}

type ActionEvent struct {
	Type      string     `json:"type"`
	EventType string     `json:"eventType"`
	Data      ActionData `json:"data"`
}

type ActionData struct {
	Claims []StorageClaim `json:"claims"`
}

type StorageClaim struct {
	Environment          int    `json:"environment"`
	PersisteStorageClaim string `json:"persistentStorageClaim"`
	BytesUsed            int    `json:"bytesUsed"`
}

// Calculate will run the storage-calculator job.
func (c *Calculator) Calculate() {
	ctx := context.Background()
	opLog := c.Log.WithName("storage").WithName("Calculator")
	listOption := &client.ListOptions{}
	// check for environments that are lagoon environments
	r1, _ := labels.NewRequirement("lagoon.sh/environmentType", "in", []string{"production", "development"})
	// and check for namespaces that have not got storagecalculator disabled
	r2, _ := labels.NewRequirement("lagoon.sh/storageCalculatorEnabled", "in", []string{"true"})
	r3, _ := labels.NewRequirement("lagoon.sh/environmentId", "exists", []string{})
	labelRequirements := []labels.Requirement{}
	labelRequirements = append(labelRequirements, *r1)
	labelRequirements = append(labelRequirements, *r2)
	labelRequirements = append(labelRequirements, *r3)
	listOption = (&client.ListOptions{}).ApplyOptions([]client.ListOption{
		client.MatchingLabelsSelector{
			Selector: labels.NewSelector().Add(labelRequirements...),
		},
	})
	namespaces := &corev1.NamespaceList{}
	if err := c.Client.List(ctx, namespaces, listOption); err != nil {
		opLog.Error(err, fmt.Sprintf("unable to get any namespaces"))
		return
	}
	for _, namespace := range namespaces.Items {
		opLog.Info(fmt.Sprintf("calculating storage for namespace %s", namespace.ObjectMeta.Name))
		p1, _ := labels.NewRequirement("lagoon.sh/storageCalculator", "in", []string{"true"})
		labelRequirements := []labels.Requirement{}
		labelRequirements = append(labelRequirements, *p1)
		podListOption := (&client.ListOptions{}).ApplyOptions([]client.ListOption{
			client.InNamespace(namespace.ObjectMeta.Name),
			client.MatchingLabelsSelector{
				Selector: labels.NewSelector().Add(labelRequirements...),
			},
		})
		pods := &corev1.PodList{}
		if err := c.Client.List(ctx, pods, podListOption); err != nil {
			opLog.Error(err, fmt.Sprintf("error getting running pvcs for namespace %s", namespace.ObjectMeta.Name))
		}
		for _, pod := range pods.Items {
			c.cleanup(ctx, opLog, &pod)
		}
		listOption := (&client.ListOptions{}).ApplyOptions([]client.ListOption{
			client.InNamespace(namespace.ObjectMeta.Name),
		})
		pvcs := &corev1.PersistentVolumeClaimList{}
		if err := c.Client.List(ctx, pvcs, listOption); err != nil {
			opLog.Error(err, fmt.Sprintf("error getting running pvcs for namespace %s", namespace.ObjectMeta.Name))
		} else {
			// check for ignore regex configuration
			ignoreRegex := c.IgnoreRegex
			if value, ok := namespace.ObjectMeta.Labels["lagoon.sh/storageCalculatorIgnoreRegex"]; ok {
				ignoreRegex = value
			}
			environmentID := 0
			if value, ok := namespace.ObjectMeta.Labels["lagoon.sh/environmentId"]; ok {
				eBytes := strings.TrimSpace(value)
				environmentID, _ = strconv.Atoi(eBytes)
			}
			// create place to store storage claim sizes
			storData := ActionData{}

			if len(pvcs.Items) == 0 {
				storData.Claims = append(storData.Claims, StorageClaim{
					Environment:          environmentID,
					PersisteStorageClaim: "none",
					BytesUsed:            0,
				})
				actionData := ActionEvent{
					Type:      "updateEnvironmentStorage",
					EventType: "environmentStorage",
					Data:      storData,
				}
				// send the calculated storage result to the api @TODO
				opLog.Info(fmt.Sprintf("no volumes in %s: %v", namespace.ObjectMeta.Name, actionData))
				// marshal and publish the result to actions-handler
				ad, _ := json.Marshal(actionData)
				if err := c.MQ.Publish("lagoon-actions", ad); err != nil {
					opLog.Error(err, "error publishing message to mq")
					continue
				}
				// no pvcs in this namespace, continue to the next one
				continue
			}
			volumeMounts := []corev1.VolumeMount{}
			volumes := []corev1.Volume{}
			// define volume mounts
			for _, pvc := range pvcs.Items {
				// check if the specified pvc is to be ignored
				if ignoreRegex != "" {
					match, _ := regexp.MatchString(ignoreRegex, pvc.ObjectMeta.Name)
					if match {
						// this pvc is not to be calculated
						continue
					}
				}
				// defined the volumes and mounts
				volumeMounts = append(volumeMounts, corev1.VolumeMount{
					Name:      pvc.ObjectMeta.Name,
					MountPath: fmt.Sprintf("/storage/%s", pvc.ObjectMeta.Name),
				})
				volumes = append(volumes, corev1.Volume{
					Name: pvc.ObjectMeta.Name,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvc.ObjectMeta.Name,
							ReadOnly:  true,
						},
					},
				})
			}
			// define the storage-calculator pod
			podName := fmt.Sprintf("storage-calculator-%s", ns.RandString(8))
			storagePod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: namespace.ObjectMeta.Name,
					Labels: map[string]string{
						"lagoon.sh/storageCalculator": "true",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:         "storage-calculator",
							Image:        c.CalculatorImage,
							Command:      []string{"sh", "-c", "while sleep 3600; do :; done"},
							VolumeMounts: volumeMounts,
							EnvFrom: []corev1.EnvFromSource{
								corev1.EnvFromSource{
									ConfigMapRef: &corev1.ConfigMapEnvSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "lagoon-env",
										},
									},
								},
							},
						},
					},
					Volumes: volumes,
				},
			}

			opLog.Info(fmt.Sprintf("creating storage-calculator pod %s/%s", namespace.ObjectMeta.Name, podName))
			if err := c.Client.Create(ctx, storagePod); err != nil {
				opLog.Error(err, fmt.Sprintf("error creating storage-calculator pod %s/%s", namespace.ObjectMeta.Name, podName))
				continue
			}
			// wait for it to be running
			if err := wait.PollImmediateWithContext(ctx, time.Second, 90*time.Second,
				c.hasRunningPod(ctx, namespace.ObjectMeta.Name, podName)); err != nil {
				opLog.Error(err, fmt.Sprintf("error starting storage-calculator pod %s/%s", namespace.ObjectMeta.Name, podName))
				c.cleanup(ctx, opLog, storagePod)
				continue
			}

			// exec in and check the volumes are mounted
			var stdin io.Reader
			_, _, err := execPod(
				podName,
				namespace.ObjectMeta.Name,
				[]string{"/bin/sh", "-c", "ls /storage"},
				stdin,
				false,
			)
			if err != nil {
				opLog.Error(err, fmt.Sprintf("error checking storage-calculator pod %s/%s for volumes", namespace.ObjectMeta.Name, podName))
				c.cleanup(ctx, opLog, storagePod)
				continue
			}

			// check pvcs for their sizes
			for _, pvc := range pvcs.Items {
				// check if the specified pvc is to be ignored
				if ignoreRegex != "" {
					match, _ := regexp.MatchString(ignoreRegex, pvc.ObjectMeta.Name)
					if match {
						// this pvc is not to be calculated
						continue
					}
				}
				var stdin io.Reader
				pvcValue, _, err := execPod(
					podName,
					namespace.ObjectMeta.Name,
					[]string{"/bin/sh", "-c", fmt.Sprintf("du -s /storage/%s | cut -f1", pvc.ObjectMeta.Name)},
					stdin,
					false,
				)
				if err != nil {
					opLog.Error(err, fmt.Sprintf("error checking storage-calculator pod %s/%s for pvc %s size", namespace.ObjectMeta.Name, podName, pvc.ObjectMeta.Name))
					c.cleanup(ctx, opLog, storagePod)
					continue
				}
				pBytes := strings.TrimSpace(pvcValue)
				pBytesInt, _ := strconv.Atoi(pBytes)
				storData.Claims = append(storData.Claims, StorageClaim{
					Environment:          environmentID,
					PersisteStorageClaim: pvc.ObjectMeta.Name,
					BytesUsed:            pBytesInt,
				})
			}
			mdbValue, _, err := execPod(
				podName,
				namespace.ObjectMeta.Name,
				[]string{"/bin/sh", "-c", `if [ "$MARIADB_HOST" ]; then mysql -N -s -h $MARIADB_HOST -u$MARIADB_USERNAME -p$MARIADB_PASSWORD -P$MARIADB_PORT -e 'SELECT ROUND(SUM(data_length + index_length) / 1024, 0) FROM information_schema.tables'; else exit 1; fi`},
				stdin,
				false,
			)
			if err != nil {
				opLog.Error(err, fmt.Sprintf("error checking storage-calculator pod %s/%s for mariadb size", namespace.ObjectMeta.Name, podName))
				c.cleanup(ctx, opLog, storagePod)
				continue
			}
			if mdbValue != "" {
				mBytes := strings.TrimSpace(mdbValue)
				mBytesInt, _ := strconv.Atoi(mBytes)
				storData.Claims = append(storData.Claims, StorageClaim{
					Environment:          environmentID,
					PersisteStorageClaim: "mariadb",
					BytesUsed:            mBytesInt,
				})
				mergePatch, _ := json.Marshal(map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]string{
							"lagoon/storage-mariadb":    mBytes,
							"lagoon.sh/storage-mariadb": mBytes,
						},
					},
				})
				if err := c.Client.Patch(ctx, &namespace, client.RawPatch(types.MergePatchType, mergePatch)); err != nil {
					opLog.Error(err, fmt.Sprintf("error patching %s", namespace.ObjectMeta.Name))
					c.cleanup(ctx, opLog, storagePod)
					continue
				}
			}
			c.cleanup(ctx, opLog, storagePod)
			// send the calculated storage result to the api @TODO

			actionData := ActionEvent{
				Type:      "updateEnvironmentStorage",
				EventType: "environmentStorage",
				Data:      storData,
			}
			opLog.Info(fmt.Sprintf("volumes from storage-calculator pod %s/%s: %v", namespace.ObjectMeta.Name, podName, actionData))
			// marshal and publish the result to actions-handler
			ad, _ := json.Marshal(actionData)
			if err := c.MQ.Publish("lagoon-actions", ad); err != nil {
				opLog.Error(err, "error publishing message to mq")
				continue
			}
		}
	}
}

func (c *Calculator) cleanup(ctx context.Context, opLog logr.Logger, storagePod *corev1.Pod) {
	opLog.Info(fmt.Sprintf("cleaning up storage-calculator pod %s/%s", storagePod.ObjectMeta.Namespace, storagePod.ObjectMeta.Name))
	if err := c.Client.Delete(ctx, storagePod); err != nil {
		opLog.Error(err, fmt.Sprintf("error deleting storage-calculator pod %s/%s", storagePod.ObjectMeta.Namespace, storagePod.ObjectMeta.Name))
	}
}

func (c *Calculator) hasRunningPod(ctx context.Context,
	namespace, pod string) wait.ConditionWithContextFunc {
	return func(context.Context) (bool, error) {
		storagePod := &corev1.Pod{}
		if err := c.Client.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      pod,
		}, storagePod); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return storagePod.Status.Phase == "Running", nil
	}
}
