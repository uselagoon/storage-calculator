package storage

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	mariadbv1 "github.com/amazeeio/dbaas-operator/apis/mariadb/v1"
	postgresv1 "github.com/amazeeio/dbaas-operator/apis/postgres/v1"
	"github.com/go-logr/logr"
	ns "github.com/uselagoon/machinery/utils/namespace"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

// volumesCalculatorPod is used to hold volume and volumemount information and node names
// this is used for creating separate storage-calculator pods for pod volumes that may
// be spread across multiple nodes
type volumesCalculatorPod struct {
	NodeName     string
	Volumes      []corev1.Volume
	VolumeMounts []corev1.VolumeMount
}

// databasesCalculatorPod holds resources for databases managed by dbaas-operator.
type databasesCalculatorPod struct {
	MariaDB    []mariadbv1.MariaDBConsumer
	PostgreSQL []postgresv1.PostgreSQLConsumer
}

// createVolumesPod creates a pod that will be used to calculate storage usage for volumes listed
// in `spn`.
func (c *Calculator) createVolumesPod(
	ctx context.Context,
	opLog logr.Logger,
	namespace corev1.Namespace,
	spn volumesCalculatorPod,
	environmentID int,
) ([]StorageClaim, error) {
	storageClaims := []StorageClaim{}
	var stdin io.Reader
	storagePod := c.getPodSpec(ctx, opLog, namespace, spn.NodeName)
	storagePod.Spec.Containers[0].VolumeMounts = spn.VolumeMounts
	storagePod.Spec.Volumes = spn.Volumes

	opLog.Info(fmt.Sprintf("creating storage-calculator pod %s/%s", namespace.Name, storagePod.Name))
	if err := c.Client.Create(ctx, storagePod); err != nil {
		return []StorageClaim{}, fmt.Errorf("error creating storage-calculator pod %s/%s: %v", namespace.Name, storagePod.Name, err)
	}

	// Wait some time for the pod to start.
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 90*time.Second, true,
		c.hasRunningPod(ctx, namespace.Name, storagePod.Name)); err != nil {
		c.cleanup(ctx, opLog, storagePod)
		return []StorageClaim{}, fmt.Errorf("error starting storage-calculator pod %s/%s: %v", namespace.Name, storagePod.Name, err)
	}

	// Check that volumes have been mounted.
	_, _, err := execPod(
		storagePod.Name,
		namespace.Name,
		[]string{"/bin/sh", "-c", "ls /storage"},
		stdin,
		false,
	)
	if err != nil {
		c.cleanup(ctx, opLog, storagePod)
		return []StorageClaim{}, fmt.Errorf("error checking storage-calculator pod %s/%s for volumes: %v", namespace.Name, storagePod.Name, err)
	}

	// Calculate the volume sizes.
	for _, vol := range spn.Volumes {
		cmd := []string{
			"/bin/sh",
			"-c",
			fmt.Sprintf("du -s /storage/%s | cut -f1", vol.Name),
		}

		podOutput, _, err := execPod(storagePod.Name, namespace.Name, cmd, stdin, false)
		if err != nil {
			opLog.Info(fmt.Sprintf("error checking storage-calculator pod %s/%s for volume %s size: %v", namespace.Name, storagePod.Name, vol.Name, err))
			continue
		}

		kiBytes := strings.TrimSpace(podOutput)
		kiBytesInt, _ := strconv.Atoi(kiBytes)
		storageClaims = append(storageClaims, StorageClaim{
			Environment:          environmentID,
			PersisteStorageClaim: vol.Name,
			KiBUsed:              uint64(kiBytesInt),
		})
	}

	c.cleanup(ctx, opLog, storagePod)

	return storageClaims, nil
}

// createDatabasePod creates a pod that will be used to calculate storage usage for databases listed
// in `services`.
func (c *Calculator) createDatabasePod(
	ctx context.Context,
	opLog logr.Logger,
	namespace corev1.Namespace,
	services databasesCalculatorPod,
	environmentID int,
) ([]StorageClaim, error) {
	storageClaims := []StorageClaim{}
	var stdin io.Reader
	storagePod := c.getPodSpec(ctx, opLog, namespace, "")

	opLog.Info(fmt.Sprintf("creating storage-calculator pod %s/%s", namespace.Name, storagePod.Name))
	if err := c.Client.Create(ctx, storagePod); err != nil {
		return []StorageClaim{}, fmt.Errorf("error creating storage-calculator pod %s/%s: %v", namespace.Name, storagePod.Name, err)
	}

	// Wait some time for the pod to start.
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 90*time.Second, true,
		c.hasRunningPod(ctx, namespace.Name, storagePod.Name)); err != nil {
		c.cleanup(ctx, opLog, storagePod)
		return []StorageClaim{}, fmt.Errorf("error starting storage-calculator pod %s/%s: %v", namespace.Name, storagePod.Name, err)
	}

	// Calculate the database sizes.
	for _, mariadb := range services.MariaDB {
		cmd := []string{
			"/bin/sh",
			"-c",
			fmt.Sprintf(
				`mysql -N -s -h "%s" -u"%s" -p"%s" -P%s -e 'SELECT ROUND(SUM(data_length + index_length) / 1024, 0) FROM information_schema.tables'`,
				mariadb.Spec.Provider.Hostname,
				mariadb.Spec.Consumer.Username,
				mariadb.Spec.Consumer.Password,
				mariadb.Spec.Provider.Port,
			)}

		podOutput, _, err := execPod(storagePod.Name, namespace.Name, cmd, stdin, false)
		if err != nil {
			opLog.Info(fmt.Sprintf("error checking storage-calculator pod %s/%s for %s size: %v", namespace.Name, storagePod.Name, mariadb.Name, err))
			continue
		}

		if podOutput != "" {
			kiBytes := strings.TrimSpace(podOutput)
			kiBytesInt, _ := strconv.Atoi(kiBytes)
			storageClaims = append(storageClaims, StorageClaim{
				Environment: environmentID,
				// Will overwrite storage of volumes with same name (extreme edge case).
				PersisteStorageClaim: mariadb.Name,
				KiBUsed:              uint64(kiBytesInt),
			})
		}
	}

	for _, postgres := range services.PostgreSQL {
		cmd := []string{
			"/bin/sh",
			"-c",
			fmt.Sprintf(
				`psql -qtA "postgresql://%s:%s@%s:%s/%s" -c "SELECT ROUND(SUM(pg_total_relation_size(c.oid)) / 1024.0, 0) AS size_kb FROM pg_class c WHERE c.relkind = 'r'"`,
				postgres.Spec.Consumer.Username,
				postgres.Spec.Consumer.Password,
				postgres.Spec.Provider.Hostname,
				postgres.Spec.Provider.Port,
				postgres.Spec.Consumer.Database,
			)}

		podOutput, _, err := execPod(storagePod.Name, namespace.Name, cmd, stdin, false)
		if err != nil {
			opLog.Info(fmt.Sprintf("error checking storage-calculator pod %s/%s for %s size: %v", namespace.Name, storagePod.Name, postgres.Name, err))
			continue
		}

		if podOutput != "" {
			kiBytes := strings.TrimSpace(podOutput)
			kiBytesInt, _ := strconv.Atoi(kiBytes)
			storageClaims = append(storageClaims, StorageClaim{
				Environment: environmentID,
				// Will overwrite storage of volumes with same name (extreme edge case).
				PersisteStorageClaim: postgres.Name,
				KiBUsed:              uint64(kiBytesInt),
			})
		}
	}

	c.cleanup(ctx, opLog, storagePod)

	return storageClaims, nil
}

func (c *Calculator) getPodSpec(
	ctx context.Context,
	opLog logr.Logger,
	namespace corev1.Namespace,
	nodeName string,
) *corev1.Pod {
	storagePod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("storage-calculator-%s", ns.RandString(8)),
			Namespace: namespace.Name,
			Labels: map[string]string{
				"lagoon.sh/storageCalculator": "true",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "storage-calculator",
					Image:   c.CalculatorImage,
					Command: []string{"sh", "-c", "while sleep 3600; do :; done"},
				},
			},
		},
	}

	// Pod may need to run on specific node to mount some volumes.
	if nodeName != "" {
		storagePod.Spec.NodeName = nodeName
	} else {
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

	// Attach lagoon env vars.
	lagoonEnvSecret := &corev1.Secret{}
	err := c.Client.Get(ctx, types.NamespacedName{
		Namespace: namespace.Name,
		Name:      "lagoon-env",
	}, lagoonEnvSecret)

	if err == nil {
		lagoonPlatformEnvSecret := &corev1.Secret{}
		err := c.Client.Get(ctx, types.NamespacedName{
			Namespace: namespace.Name,
			Name:      "lagoon-platform-env",
		}, lagoonPlatformEnvSecret)

		if err != nil {
			opLog.Info(fmt.Sprintf("no lagoon-platform-env secret %s/%s", namespace.Name, storagePod.Name))
		} else {
			storagePod.Spec.Containers[0].EnvFrom = append(storagePod.Spec.Containers[0].EnvFrom, corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "lagoon-platform-env",
					},
				},
			})
		}

		storagePod.Spec.Containers[0].EnvFrom = append(storagePod.Spec.Containers[0].EnvFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: "lagoon-env",
				},
			},
		})
	} else {
		// Env vars were stored in a ConfigMap in previous Lagoon versions.
		lagoonEnvConfigMap := &corev1.ConfigMap{}
		err := c.Client.Get(ctx, types.NamespacedName{
			Namespace: namespace.Name,
			Name:      "lagoon-env",
		}, lagoonEnvConfigMap)

		if err != nil {
			opLog.Info(fmt.Sprintf("no lagoon-env secret or configmap %s/%s", namespace.Name, storagePod.Name))
		} else {
			storagePod.Spec.Containers[0].EnvFrom = append(storagePod.Spec.Containers[0].EnvFrom, corev1.EnvFromSource{
				ConfigMapRef: &corev1.ConfigMapEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "lagoon-env",
					},
				},
			})
		}
	}

	return storagePod
}
