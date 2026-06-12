package namespace

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/uselagoon/storage-calculator/internal/storage"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// NamespaceReconciler reconciles namespaces for running storage calculators on demand
type NamespaceReconciler struct {
	client.Client
	Log     logr.Logger
	Scheme  *runtime.Scheme
	Storage *storage.Calculator
}

func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	opLog := r.Log.WithValues("namespace", req.NamespacedName)

	var namespace corev1.Namespace
	if err := r.Get(ctx, req.NamespacedName, &namespace); err != nil {
		return ctrl.Result{}, ignoreNotFound(err)
	}

	if val, ok := namespace.Labels["storage.lagoon.sh/calculate"]; ok && val == "true" {
		opLog.Info(fmt.Sprintf("Running storage calculator on environment %s", namespace.Name))
		r.Storage.CalculateNamespace(ctx, namespace, opLog)
		nsMergePatch, _ := json.Marshal(map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": map[string]*string{
					"storage.lagoon.sh/calculate": nil,
				},
			},
		})
		if err := r.Patch(ctx, &namespace, client.RawPatch(types.MergePatchType, nsMergePatch)); err != nil {
			opLog.Info(fmt.Sprintf("Error patching namespace %s -%v", namespace.Name, err))
		}
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the watch on the namespace resource with an event filter (see predicates.go)
func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		WithEventFilter(NamespacePredicates{}).
		Complete(r)
}

// will ignore not found errors
func ignoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
