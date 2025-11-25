package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	mariadbv1 "github.com/amazeeio/dbaas-operator/apis/mariadb/v1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

// checkDatabasesCreatePods calculates storage usage for databases managed by dbaas-operator.
// Databases running as a pod are handled by the volumes calculator.
func (c *Calculator) checkDatabasesCreatePods(
	ctx context.Context,
	opLog logr.Logger,
	namespace corev1.Namespace,
) (int, error) {
	opLog = opLog.WithName("Databases")
	ignoreRegex := c.IgnoreRegex
	if value, ok := namespace.Labels["lagoon.sh/storageCalculatorIgnoreRegex"]; ok {
		ignoreRegex = value
	}

	environmentID := 0
	if value, ok := namespace.Labels["lagoon.sh/environmentId"]; ok {
		environmentID, _ = strconv.Atoi(strings.TrimSpace(value))
	}

	// Check for managed MariaDB databases.
	mariadbs := []mariadbv1.MariaDBConsumer{}
	listOption := (&client.ListOptions{}).ApplyOptions([]client.ListOption{
		client.InNamespace(namespace.Name),
	})
	mariadbList := &mariadbv1.MariaDBConsumerList{}
	if err := c.Client.List(ctx, mariadbList, listOption); err != nil {
		if !meta.IsNoMatchError(err) {
			opLog.Error(err, fmt.Sprintf("error getting mariadbconsumer for namespace %s", namespace.Name))
		}
	} else {
		for _, mariadb := range mariadbList.Items {
			if ignoreRegex != "" {
				match, _ := regexp.MatchString(ignoreRegex, mariadb.Name)
				if match {
					opLog.Info(fmt.Sprintf("ignoring mariadbconsumer %s", mariadb.Name))
					continue
				}
			}
			mariadbs = append(mariadbs, mariadb)
		}
	}

	if len(mariadbs) == 0 {
		opLog.Info(fmt.Sprintf("no databases in %s", namespace.Name))
		return 0, nil
	}

	// Calculate storage for all databases.
	services := databasesCalculatorPod{
		MariaDB: mariadbs,
	}
	storageClaims, err := c.createDatabasePod(ctx, opLog, namespace, services, environmentID)
	if err != nil {
		return 0, fmt.Errorf("error calculating databases storage: %w", err)
	}

	if len(storageClaims) == 0 {
		return 0, nil
	}

	// Report storage sizes for all databases.
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
		return 0, fmt.Errorf("error publishing databases storage to mq: %v", err)
	}

	return len(storageClaims), nil
}
