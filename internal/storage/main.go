package storage

import (
	"context"
	"fmt"
	"strconv"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/uselagoon/storage-calculator/internal/broker"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
// +kubebuilder:rbac:groups="",resources=secrets,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=list;get;watch

// Calculator handles collecting storage calculator information
type Calculator struct {
	Client          client.Client
	MQ              *broker.MQ
	Log             logr.Logger
	Scheme          *runtime.Scheme
	IgnoreRegex     string
	CalculatorImage string
	Debug           bool
	ExportMetrics   bool
	PromStorage     *prometheus.GaugeVec
}

type ActionEvent struct {
	Type      string     `json:"type"`
	EventType string     `json:"eventType"`
	Data      ActionData `json:"data"`
	Meta      MetaData   `json:"meta,omitempty"`
}

func (ae *ActionEvent) ExportMetrics(promStorage *prometheus.GaugeVec) {
	for _, claim := range ae.Data.Claims {
		promStorage.With(prometheus.Labels{
			"claimenv":         strconv.Itoa(claim.Environment),
			"claimpvc":         claim.PersisteStorageClaim,
			"project":          ae.Meta.Project,
			"environment":      ae.Meta.Environment,
			"lagoon_namespace": ae.Meta.Namespace,
		}).Set(float64(claim.KiBUsed))
	}
}

type MetaData struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Namespace   string `json:"namespace"`
}

type ActionData struct {
	Claims []StorageClaim `json:"claims"`
}

type StorageClaim struct {
	Environment          int    `json:"environment"`
	PersisteStorageClaim string `json:"persistentStorageClaim"`
	KiBUsed              uint64 `json:"kibUsed"`
}

// Calculate will run the storage-calculator job.
func (c *Calculator) Calculate() {
	ctx := context.Background()
	opLog := c.Log.WithName("storage").WithName("Calculator")
	// check for environments that are lagoon environments
	r1, _ := labels.NewRequirement("lagoon.sh/environmentType", "in", []string{"production", "development"})
	// and check for namespaces that have not got storagecalculator disabled
	r2, _ := labels.NewRequirement("lagoon.sh/storageCalculatorEnabled", "in", []string{"true"})
	r3, _ := labels.NewRequirement("lagoon.sh/environmentId", "exists", []string{})
	labelRequirements := []labels.Requirement{}
	labelRequirements = append(labelRequirements, *r1)
	labelRequirements = append(labelRequirements, *r2)
	labelRequirements = append(labelRequirements, *r3)
	listOption := (&client.ListOptions{}).ApplyOptions([]client.ListOption{
		client.MatchingLabelsSelector{
			Selector: labels.NewSelector().Add(labelRequirements...),
		},
	})
	namespaces := &corev1.NamespaceList{}
	if err := c.Client.List(ctx, namespaces, listOption); err != nil {
		opLog.Error(err, "unable to get any namespaces")
		return
	}
	for _, namespace := range namespaces.Items {
		err := c.checkVolumesCreatePods(ctx, opLog, namespace)
		if err != nil {
			continue
		}
	}
}

func (c *Calculator) cleanup(
	ctx context.Context,
	opLog logr.Logger,
	storagePod *corev1.Pod,
) {
	opLog.Info(fmt.Sprintf("cleaning up storage-calculator pod %s/%s", storagePod.Namespace, storagePod.Name))
	if err := c.Client.Delete(ctx, storagePod); err != nil {
		opLog.Error(err, fmt.Sprintf("error deleting storage-calculator pod %s/%s", storagePod.Namespace, storagePod.Name))
	}
}

func (c *Calculator) hasRunningPod(
	ctx context.Context,
	namespace,
	pod string,
) wait.ConditionWithContextFunc {
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
