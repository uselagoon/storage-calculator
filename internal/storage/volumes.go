package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

func (c *Calculator) checkVolumesCreatePods(
	ctx context.Context,
	opLog logr.Logger,
	namespace corev1.Namespace,
) (int, error) {
	opLog = opLog.WithName("Volumes")
	ignoreRegex := c.IgnoreRegex
	if value, ok := namespace.Labels["lagoon.sh/storageCalculatorIgnoreRegex"]; ok {
		ignoreRegex = value
	}

	environmentID := 0
	if value, ok := namespace.Labels["lagoon.sh/environmentId"]; ok {
		environmentID, _ = strconv.Atoi(strings.TrimSpace(value))
	}

	// Check for persistent volumes.
	pvcs := []corev1.PersistentVolumeClaim{}
	listOption := (&client.ListOptions{}).ApplyOptions([]client.ListOption{
		client.InNamespace(namespace.Name),
	})
	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := c.Client.List(ctx, pvcList, listOption); err != nil {
		opLog.Error(err, fmt.Sprintf("error getting pvcs for namespace %s", namespace.Name))
		return 0, nil
	} else {
		for _, pvc := range pvcList.Items {
			if ignoreRegex != "" {
				match, _ := regexp.MatchString(ignoreRegex, pvc.Name)
				if match {
					opLog.Info(fmt.Sprintf("ignoring volume %s", pvc.Name))
					continue
				}
			}
			pvcs = append(pvcs, pvc)
		}
	}

	if len(pvcs) == 0 {
		opLog.Info(fmt.Sprintf("no volumes in %s", namespace.Name))
		return 0, nil
	}

	// work out how many storage-calculator pods are required for this environment
	storagePodPerNode := make(map[string]volumesCalculatorPod)
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}
	for _, pvc := range pvcList.Items {
		// check if the specified pvc is to be ignored
		if ignoreRegex != "" {
			match, _ := regexp.MatchString(ignoreRegex, pvc.Name)
			if match {
				// this pvc is not to be calculated
				continue
			}
		}
		// check if the volume has the accessmode of readwriteonce, if it does then try determine which node the
		// the volume is currently mounted to
		isRWO := false
		for _, am := range pvc.Spec.AccessModes {
			if am == corev1.ReadWriteOnce {
				isRWO = true
			}
		}
		if isRWO {
			// this pvc is a rwo, check which node the volume is on
			// if there are multiple pvcs on this node they will get appended to the node map
			// otherwise if they are spread across multiple nodes they will get up on those specific nodes
			podNode, err := c.getNodeForRWOPV(ctx, opLog, namespace, pvc.Labels["lagoon.sh/service-type"], pvc.Name)
			if err != nil {
				if strings.Contains(err.Error(), "volume is not attached to any pods") {
					opLog.Info(fmt.Sprintf("%s %v", pvc.Name, err))
					// append to the general namespace based volumes
					volumes = append(volumes, corev1.Volume{
						Name: pvc.Name,
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: pvc.Name,
								ReadOnly:  true,
							},
						},
					})
					volumeMounts = append(volumeMounts, corev1.VolumeMount{
						Name:      pvc.Name,
						MountPath: fmt.Sprintf("/storage/%s", pvc.Name),
					})
				} else {
					// otherwise skip it as there could be other issues
					opLog.Info(fmt.Sprintf("error checking volume, skipping: %v", err))
				}
				continue
			}
			if spn, ok := storagePodPerNode[podNode]; ok {
				// if this is the first time we are seeing the node, add the first volume to it
				spn.VolumeMounts = append(spn.VolumeMounts, corev1.VolumeMount{
					Name:      pvc.Name,
					MountPath: fmt.Sprintf("/storage/%s", pvc.Name),
				})
				spn.Volumes = append(spn.Volumes, corev1.Volume{
					Name: pvc.Name,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvc.Name,
							ReadOnly:  true,
						},
					},
				})
				storagePodPerNode[podNode] = spn
			} else {
				// additional volumes get appended if they're on the same node as the other volumes
				storagePodPerNode[podNode] = volumesCalculatorPod{
					NodeName: podNode,
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      pvc.Name,
							MountPath: fmt.Sprintf("/storage/%s", pvc.Name),
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: pvc.Name,
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvc.Name,
									ReadOnly:  true,
								},
							},
						},
					},
				}
			}
		} else {
			// otherwise it has ReadWriteMany volume to append to the existing volumes
			// these can be created on any node
			volumes = append(volumes, corev1.Volume{
				Name: pvc.Name,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvc.Name,
						ReadOnly:  true,
					},
				},
			})
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      pvc.Name,
				MountPath: fmt.Sprintf("/storage/%s", pvc.Name),
			})
		}
	}

	// move all the general volumes to one of the pods that would be created on a specific node
	// to reduce the number of storage-calculator pods being created
	if len(storagePodPerNode) >= 1 {
		for k := range storagePodPerNode {
			// add the general volumes to an existing storagepod
			volumes := append(storagePodPerNode[k].Volumes, volumes...)
			volumeMounts := append(storagePodPerNode[k].VolumeMounts, volumeMounts...)
			storagePodPerNode[k] = volumesCalculatorPod{
				NodeName:     storagePodPerNode[k].NodeName,
				Volumes:      volumes,
				VolumeMounts: volumeMounts,
			}
			// drop out, only needs to be on the first one that is in the slice if there are more than 1
			break
		}
	} else {
		// there aren't any rwo volumes, so just create a single pod for all general volumes
		storagePodPerNode[namespace.Name] = volumesCalculatorPod{
			Volumes:      volumes,
			VolumeMounts: volumeMounts,
		}
	}

	// now create the required pods
	storageClaims := []StorageClaim{}
	for _, spn := range storagePodPerNode {
		volClaims, err := c.createVolumesPod(ctx, opLog, namespace, spn, environmentID)
		if err != nil {
			opLog.Error(err, "create storage-calculator pod error")
			continue
		}

		storageClaims = append(storageClaims, volClaims...)
	}

	if len(storageClaims) == 0 {
		return 0, nil
	}

	// Report storage sizes for all volumes.
	actionData := ActionEvent{
		Type:      "updateEnvironmentStorage",
		EventType: "environmentStorage",
		Data: ActionData{
			Claims: storageClaims,
		},
		Meta: MetaData{
			Namespace:   namespace.Name,
			Project:     namespace.Labels["lagoon.sh/project"],
			Environment: namespace.Labels["lagoon.sh/environment"],
		},
	}
	opLog.Info(fmt.Sprintf("storage in %s: %v", namespace.Name, actionData))

	if c.ExportMetrics {
		actionData.ExportMetrics(c.PromStorage)
	}

	actionDataJSON, _ := json.Marshal(actionData)
	if err := c.MQ.Publish("lagoon-actions", actionDataJSON); err != nil {
		return 0, fmt.Errorf("error publishing volumes storage to mq: %v", err)
	}

	return len(storageClaims), nil

}

// getNodeForRWOPV attempts to check which node a ReadWriteOnce PV is attached to, to make sure that a storage-calculator
// pod starts on the same node to take advantage of same node access to the volume
func (c *Calculator) getNodeForRWOPV(ctx context.Context, opLog logr.Logger, namespace corev1.Namespace, sType, cName string) (string, error) {
	p1, _ := labels.NewRequirement("lagoon.sh/service-type", "in", []string{sType})
	labelRequirements := []labels.Requirement{}
	labelRequirements = append(labelRequirements, *p1)
	podListOption := (&client.ListOptions{}).ApplyOptions([]client.ListOption{
		client.InNamespace(namespace.Name),
		client.MatchingLabelsSelector{
			Selector: labels.NewSelector().Add(labelRequirements...),
		},
	})
	pods := &corev1.PodList{}
	if err := c.Client.List(ctx, pods, podListOption); err != nil {
		opLog.Error(err, fmt.Sprintf("error getting running pvcs for namespace %s", namespace.Name))
		return "", fmt.Errorf("error getting running pvcs for namespace %s", namespace.Name)
	}
	for _, pod := range pods.Items {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				if volume.PersistentVolumeClaim.ClaimName == cName {
					return pod.Spec.NodeName, nil
				}
			}
		}
	}
	return "", fmt.Errorf("volume is not attached to any pods")
}
