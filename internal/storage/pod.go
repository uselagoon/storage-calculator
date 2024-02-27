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
	ns "github.com/uselagoon/machinery/utils/namespace"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

// storageCalculatorPod is used to hold volume and volumemount information and node names
// this is used for creating separate storage-calculator pods for pod volumes that may
// be spread across multiple nodes
type storageCalculatorPod struct {
	NodeName     string
	Volumes      []corev1.Volume
	VolumeMounts []corev1.VolumeMount
}

func (c *Calculator) createStoragePod(
	ctx context.Context,
	opLog logr.Logger,
	namespace corev1.Namespace,
	spn storageCalculatorPod,
	environmentID int,
	ignoreRegex string,
	checkedDatabase *bool,
) error {
	storData := ActionData{}
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
					VolumeMounts: spn.VolumeMounts,
					EnvFrom: []corev1.EnvFromSource{
						{
							ConfigMapRef: &corev1.ConfigMapEnvSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "lagoon-env",
								},
							},
						},
					},
				},
			},
			Volumes: spn.Volumes,
		},
	}

	// if the storage pod has to be assigned to a specific node due to ReadWriteOnce, set the nodename here
	if spn.NodeName != "" {
		storagePod.Spec.NodeName = spn.NodeName
	} else {
		// otherwise we can try and set this to start on spot instances if they are existing
		storagePod.Spec.Affinity = &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
					{
						Preference: corev1.NodeSelectorTerm{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "lagoon.sh/spot",
									Operator: corev1.NodeSelectorOpExists,
								},
							},
						},
						Weight: 1,
					},
				},
			},
		}
		storagePod.Spec.Tolerations = []corev1.Toleration{
			{
				Key:      "lagoon.sh/spot",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectPreferNoSchedule,
			}, {
				Key:      "lagoon.sh/spot",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			},
		}
	}

	// create the pod in the cluster
	opLog.Info(fmt.Sprintf("creating storage-calculator pod %s/%s", namespace.ObjectMeta.Name, podName))
	if err := c.Client.Create(ctx, storagePod); err != nil {
		return fmt.Errorf("error creating storage-calculator pod %s/%s: %v", namespace.ObjectMeta.Name, podName, err)
	}

	// wait for it to be running, or time out waiting to start the pod
	if err := wait.PollImmediateWithContext(ctx, time.Second, 90*time.Second,
		c.hasRunningPod(ctx, namespace.ObjectMeta.Name, podName)); err != nil {
		c.cleanup(ctx, opLog, storagePod)
		return fmt.Errorf("error starting storage-calculator pod %s/%s: %v", namespace.ObjectMeta.Name, podName, err)
	}

	// exec in and check the volumes are mounted firstly
	var stdin io.Reader
	_, _, err := execPod(
		podName,
		namespace.ObjectMeta.Name,
		[]string{"/bin/sh", "-c", "ls /storage"},
		stdin,
		false,
	)
	if err != nil {
		c.cleanup(ctx, opLog, storagePod)
		return fmt.Errorf("error checking storage-calculator pod %s/%s for volumes: %v", namespace.ObjectMeta.Name, podName, err)
	}

	// check pvcs for their sizes
	for _, vol := range spn.Volumes {
		// check if the specified pvc is to be ignored
		if ignoreRegex != "" {
			match, _ := regexp.MatchString(ignoreRegex, vol.Name)
			if match {
				// this pvc is not to be calculated
				continue
			}
		}

		// exec into the pod and check the storage size using du
		var stdin io.Reader
		pvcValue, _, err := execPod(
			podName,
			namespace.ObjectMeta.Name,
			[]string{"/bin/sh", "-c", fmt.Sprintf("du -s /storage/%s | cut -f1", vol.Name)},
			stdin,
			false,
		)
		if err != nil {
			c.cleanup(ctx, opLog, storagePod)
			return fmt.Errorf("error checking storage-calculator pod %s/%s for pvc %s size: %v", namespace.ObjectMeta.Name, podName, vol.Name, err)
		}
		pBytes := strings.TrimSpace(pvcValue)
		pBytesInt, _ := strconv.Atoi(pBytes)
		storData.Claims = append(storData.Claims, StorageClaim{
			Environment:          environmentID,
			PersisteStorageClaim: vol.Name,
			BytesUsed:            uint64(pBytesInt),
			KiBUsed:              uint64(pBytesInt),
		})
	}

	if !*checkedDatabase {
		// this could be improved to handle more, for now this replicates existing functionality
		// collect the size of the db size from a dbaas
		mdbValue, _, err := execPod(
			podName,
			namespace.ObjectMeta.Name,
			[]string{"/bin/sh", "-c", `if [ "$MARIADB_HOST" ]; then mysql -N -s -h $MARIADB_HOST -u$MARIADB_USERNAME -p$MARIADB_PASSWORD -P$MARIADB_PORT -e 'SELECT ROUND(SUM(data_length + index_length) / 1024, 0) FROM information_schema.tables'; else exit 0; fi`},
			stdin,
			false,
		)
		if err != nil {
			opLog.Info(fmt.Sprintf("error checking storage-calculator pod %s/%s for database size", namespace.ObjectMeta.Name, podName))
		}
		if mdbValue != "" {
			// if there is a value returned that isn't "no database"
			// then storedata against the event data
			mBytes := strings.TrimSpace(mdbValue)
			mBytesInt, _ := strconv.Atoi(mBytes)
			storData.Claims = append(storData.Claims, StorageClaim{
				Environment:          environmentID,
				PersisteStorageClaim: "mariadb",
				BytesUsed:            uint64(mBytesInt),
				KiBUsed:              uint64(mBytesInt),
			})
			// and attempt to patch the namespace with the labels
			mergePatch, _ := json.Marshal(map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]string{
						"lagoon/storage-mariadb":    mBytes,
						"lagoon.sh/storage-mariadb": mBytes,
					},
				},
			})
			if err := c.Client.Patch(ctx, &namespace, client.RawPatch(types.MergePatchType, mergePatch)); err != nil {
				c.cleanup(ctx, opLog, storagePod)
				return fmt.Errorf("error patching namespace %s: %v", namespace.ObjectMeta.Name, err)
			}
		}
		// let the process know that we already checked the database so any additional storage-calculator pods for this namespace
		// don't attempt to check it again
		*checkedDatabase = true
	}
	c.cleanup(ctx, opLog, storagePod)

	// send the calculated storage result to the api
	actionData := ActionEvent{
		Type:      "updateEnvironmentStorage",
		EventType: "environmentStorage",
		Data:      storData,
		Meta: MetaData{
			Namespace:   namespace.ObjectMeta.Name,
			Project:     namespace.ObjectMeta.Labels["lagoon.sh/project"],
			Environment: namespace.ObjectMeta.Labels["lagoon.sh/environment"],
		},
	}
	opLog.Info(fmt.Sprintf("volumes from storage-calculator pod %s/%s: %v", namespace.ObjectMeta.Name, podName, actionData))
	// export metrics if enabled
	if c.ExportMetrics {
		actionData.ExportMetrics(c.PromStorage)
	}
	// marshal and publish the result to actions-handler
	ad, _ := json.Marshal(actionData)
	if err := c.MQ.Publish("lagoon-actions", ad); err != nil {
		return fmt.Errorf("error publishing message to mq: %v", err)
	}
	return nil
}
