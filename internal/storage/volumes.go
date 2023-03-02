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
) error {
	opLog.Info(fmt.Sprintf("calculating storage for namespace %s", namespace.ObjectMeta.Name))

	// check for existing storage-calculator pods that may be stuck and clean them up before proceeding
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
		opLog.Error(err, fmt.Sprintf("error getting running pods for namespace %s", namespace.ObjectMeta.Name))
	}
	for _, pod := range pods.Items {
		c.cleanup(ctx, opLog, &pod)
	}

	// get a list of the persistent volume claims in the namespace
	listOption := (&client.ListOptions{}).ApplyOptions([]client.ListOption{
		client.InNamespace(namespace.ObjectMeta.Name),
	})
	pvcs := &corev1.PersistentVolumeClaimList{}
	if err := c.Client.List(ctx, pvcs, listOption); err != nil {
		opLog.Error(err, fmt.Sprintf("error getting pvcs for namespace %s", namespace.ObjectMeta.Name))
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
		if len(pvcs.Items) == 0 {
			// if there are no pvcs, then set the storage for none to 0
			storData := ActionData{}
			storData.Claims = append(storData.Claims, StorageClaim{
				Environment:          environmentID,
				PersisteStorageClaim: "none",
				BytesUsed:            0,
			})
			actionData := ActionEvent{
				Type:      "updateEnvironmentStorage",
				EventType: "environmentStorage",
				Data:      storData,
				Meta: MetaData{
					Project:     namespace.ObjectMeta.Labels["lagoon.sh/project"],
					Environment: namespace.ObjectMeta.Labels["lagoon.sh/environment"],
				},
			}
			// send the calculated storage result to the api @TODO
			opLog.Info(fmt.Sprintf("no volumes in %s: %v", namespace.ObjectMeta.Name, actionData))
			// export metrics if enabled
			if c.ExportMetrics {
				actionData.ExportMetrics(c.PromStorage)
			}
			// marshal and publish the result to actions-handler
			ad, _ := json.Marshal(actionData)
			if err := c.MQ.Publish("lagoon-actions", ad); err != nil {
				opLog.Error(err, "error publishing message to mq")
				return err
			}
			// no pvcs in this namespace, continue to the next one
			return fmt.Errorf("no pvcs in namespace %s", namespace.ObjectMeta.Name)
		}

		// work out how many storage-calculator pods are required for this environment
		storagePodPerNode := make(map[string]storageCalculatorPod)
		volumeMounts := []corev1.VolumeMount{}
		volumes := []corev1.Volume{}
		for _, pvc := range pvcs.Items {
			// check if the specified pvc is to be ignored
			if ignoreRegex != "" {
				match, _ := regexp.MatchString(ignoreRegex, pvc.ObjectMeta.Name)
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
				podNode, err := c.getNodeForRWOPV(ctx, opLog, namespace, pvc.Labels["lagoon.sh/service-type"], pvc.ObjectMeta.Name)
				if err != nil {
					if strings.Contains(err.Error(), "volume is not attached to any pods") {
						opLog.Info(fmt.Sprintf("%v", err))
						// append to the general namespace based volumes
						volumes = append(volumes, corev1.Volume{
							Name: pvc.ObjectMeta.Name,
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvc.ObjectMeta.Name,
									ReadOnly:  true,
								},
							},
						})
						volumeMounts = append(volumeMounts, corev1.VolumeMount{
							Name:      pvc.ObjectMeta.Name,
							MountPath: fmt.Sprintf("/storage/%s", pvc.ObjectMeta.Name),
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
						Name:      pvc.ObjectMeta.Name,
						MountPath: fmt.Sprintf("/storage/%s", pvc.ObjectMeta.Name),
					})
					spn.Volumes = append(spn.Volumes, corev1.Volume{
						Name: pvc.ObjectMeta.Name,
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: pvc.ObjectMeta.Name,
								ReadOnly:  true,
							},
						},
					})
					storagePodPerNode[podNode] = spn
				} else {
					// additional volumes get appended if they're on the same node as the other volumes
					storagePodPerNode[podNode] = storageCalculatorPod{
						NodeName: podNode,
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      pvc.ObjectMeta.Name,
								MountPath: fmt.Sprintf("/storage/%s", pvc.ObjectMeta.Name),
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: pvc.ObjectMeta.Name,
								VolumeSource: corev1.VolumeSource{
									PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
										ClaimName: pvc.ObjectMeta.Name,
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
					Name: pvc.ObjectMeta.Name,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvc.ObjectMeta.Name,
							ReadOnly:  true,
						},
					},
				})
				volumeMounts = append(volumeMounts, corev1.VolumeMount{
					Name:      pvc.ObjectMeta.Name,
					MountPath: fmt.Sprintf("/storage/%s", pvc.ObjectMeta.Name),
				})
			}
		}

		// move all the general volumes to one of the pods that would be created on a specific node
		// to reduce the number of storage-calculator pods being created
		if len(storagePodPerNode) >= 1 {
			for k := range storagePodPerNode {
				// add the general volumes to an existing storagepod
				vols := append(storagePodPerNode[k].Volumes, volumes...)
				volMounts := append(storagePodPerNode[k].VolumeMounts, volumeMounts...)
				storagePodPerNode[k] = storageCalculatorPod{
					NodeName:     storagePodPerNode[k].NodeName,
					Volumes:      vols,
					VolumeMounts: volMounts,
				}
				// drop out, only needs to be on the first one that is in the slice if there are more than 1
				break
			}
		} else {
			// there aren't any rwo volumes, so just create a single pod for all general volumes
			storagePodPerNode[namespace.ObjectMeta.Name] = storageCalculatorPod{
				Volumes:      volumes,
				VolumeMounts: volumeMounts,
			}
		}

		// now create the required pods
		checkedDatabase := false
		for _, spn := range storagePodPerNode {
			if err := c.createStoragePod(ctx, opLog, namespace, spn, environmentID, ignoreRegex, &checkedDatabase); err != nil {
				opLog.Error(err, "create storage-calculator pod error")
			}
		}
	}
	return nil
}

// getNodeForRWOPV attempts to check which node a ReadWriteOnce PV is attached to, to make sure that a storage-calculator
// pod starts on the same node to take advantage of same node access to the volume
func (c *Calculator) getNodeForRWOPV(ctx context.Context, opLog logr.Logger, namespace corev1.Namespace, sType, cName string) (string, error) {
	p1, _ := labels.NewRequirement("lagoon.sh/service-type", "in", []string{sType})
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
		return "", fmt.Errorf("error getting running pvcs for namespace %s", namespace.ObjectMeta.Name)
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
