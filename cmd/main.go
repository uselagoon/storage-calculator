/*
Copyright 2023.

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

package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	"github.com/prometheus/client_golang/prometheus"
	"github.com/uselagoon/storage-calculator/internal/broker"
	"github.com/uselagoon/storage-calculator/internal/storage"

	"github.com/robfig/cron/v3"
	"github.com/uselagoon/machinery/utils/variables"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	// "github.com/cheshir/go-mq"
	mariadbv1 "github.com/amazeeio/dbaas-operator/apis/mariadb/v1"
	mq "github.com/cheshir/go-mq/v2"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	//+kubebuilder:scaffold:imports
)

var (
	scheme       = runtime.NewScheme()
	setupLog     = ctrl.Log.WithName("setup")
	prom_storage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lagoon_storage_calculator_kilobytes",
			Help: "The lagoon storage calculator kilobytes bytes",
		},
		[]string{"claimenv", "claimpvc", "project", "environment", "lagoon_namespace"},
	)
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(mariadbv1.AddToScheme(scheme))

	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var enableLeaderElection bool
	var probeAddr string
	var ignoreRegex string
	var calculatorCron string
	var lagoonAppID string
	var storageCalculatorImage string

	var mqUser string
	var mqPass string
	var mqHost string
	var mqTLS bool
	var mqVerify bool
	var mqCACert string
	var mqClientCert string
	var mqClientKey string
	var mqWorkers int
	var rabbitRetryInterval int
	var exportPrometheusMetrics bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&calculatorCron, "calculator-cron", "5 */12 * * *", "The cron definition for how often to run the storage-calculator.")
	flag.StringVar(&ignoreRegex, "ignore-regex", "", "Regex pattern to match for which types of storage to ignore checking (eg 'solr|redis').")
	flag.StringVar(&mqUser, "rabbitmq-username", "guest",
		"The username of the rabbitmq user.")
	flag.StringVar(&mqPass, "rabbitmq-password", "guest",
		"The password for the rabbitmq user.")
	flag.StringVar(&mqHost, "rabbitmq-hostname", "localhost:5672",
		"The hostname:port for the rabbitmq host.")
	flag.BoolVar(&mqTLS, "rabbitmq-tls", false,
		"To use amqps instead of amqp.")
	flag.BoolVar(&mqVerify, "rabbitmq-verify", false,
		"To verify rabbitmq peer connection.")
	flag.StringVar(&mqCACert, "rabbitmq-cacert", "",
		"The path to the ca certificate")
	flag.StringVar(&mqClientCert, "rabbitmq-clientcert", "",
		"The path to the client certificate")
	flag.StringVar(&mqClientKey, "rabbitmq-clientkey", "",
		"The path to the client key")
	flag.IntVar(&mqWorkers, "rabbitmq-queue-workers", 1,
		"The number of workers to start with.")
	flag.IntVar(&rabbitRetryInterval, "rabbitmq-retry-interval", 30,
		"The retry interval for rabbitmq.")
	flag.StringVar(&lagoonAppID, "lagoon-app-id", "storage-calculator",
		"The appID to use that will be sent with messages.")
	flag.StringVar(&storageCalculatorImage, "storage-calculator-image", "imagecache.amazeeio.cloud/amazeeio/alpine-mysql-client",
		"The image to use for storage-calculator pods.")
	flag.BoolVar(&exportPrometheusMetrics, "prometheus-metrics", false, "Export prometheus metrics.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	calculatorCron = variables.GetEnv("CALCULATOR_CRON", calculatorCron)
	ignoreRegex = variables.GetEnv("LAGOON_STORAGE_IGNORE_REGEX", ignoreRegex)
	mqUser = variables.GetEnv("RABBITMQ_USERNAME", mqUser)
	mqPass = variables.GetEnv("RABBITMQ_PASSWORD", mqPass)
	mqHost = variables.GetEnv("RABBITMQ_HOSTNAME", mqHost)
	mqTLS = variables.GetEnvBool("RABBITMQ_TLS", mqTLS)
	mqCACert = variables.GetEnv("RABBITMQ_CACERT", mqCACert)
	mqClientCert = variables.GetEnv("RABBITMQ_CLIENTCERT", mqClientCert)
	mqClientKey = variables.GetEnv("RABBITMQ_CLIENTKEY", mqClientKey)
	mqVerify = variables.GetEnvBool("RABBITMQ_VERIFY", mqVerify)

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}
	tlsOpts := []func(*tls.Config){}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "14209f0a.uselagoon.sh",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	brokerDSN := fmt.Sprintf("amqp://%s:%s@%s", mqUser, mqPass, mqHost)
	if mqTLS {
		verify := "verify_none"
		if mqVerify {
			verify = "verify_peer"
		}
		brokerDSN = fmt.Sprintf("amqps://%s:%s@%s?verify=%s", mqUser, mqPass, mqHost, verify)
		if mqCACert != "" {
			brokerDSN = fmt.Sprintf("%s&cacertfile=%s", brokerDSN, mqCACert)
		}
		if mqClientCert != "" {
			brokerDSN = fmt.Sprintf("%s&certfile=%s", brokerDSN, mqClientCert)
		}
		if mqClientKey != "" {
			brokerDSN = fmt.Sprintf("%s&keyfile=%s", brokerDSN, mqClientKey)
		}
	}
	config := mq.Config{
		ReconnectDelay: time.Duration(rabbitRetryInterval) * time.Second,
		Exchanges: mq.Exchanges{
			{
				Name: "lagoon-actions",
				Type: "direct",
				Options: mq.Options{
					"durable":       true,
					"delivery_mode": "2",
					"headers":       "",
					"content_type":  "",
				},
			},
		},
		Queues: mq.Queues{
			{
				Name:     "lagoon-actions:items",
				Exchange: "lagoon-actions",
				Options: mq.Options{
					"durable":       true,
					"delivery_mode": "2",
					"headers":       "",
					"content_type":  "",
				},
			},
		},
		Producers: mq.Producers{
			{
				Name:     "lagoon-actions",
				Exchange: "lagoon-actions",
				Options: mq.Options{
					"delivery_mode": "2",
					"headers":       "",
					"content_type":  "",
				},
			},
		},
		DSN: brokerDSN,
	}

	messaging := broker.NewMQ(
		config,
		false,
	)

	if exportPrometheusMetrics {
		metrics.Registry.MustRegister(prom_storage)
	}

	// setup the handler with the k8s and lagoon clients
	storage := &storage.Calculator{
		Client:          mgr.GetClient(),
		MQ:              messaging,
		Log:             ctrl.Log,
		IgnoreRegex:     ignoreRegex,
		CalculatorImage: storageCalculatorImage,
		Debug:           false,
		ExportMetrics:   exportPrometheusMetrics,
		PromStorage:     prom_storage,
	}
	c := cron.New()
	// add the cronjobs we need.
	// Storage Calculator
	_, err = c.AddFunc(calculatorCron, func() {
		storage.Calculate()
	})
	if err != nil {
		setupLog.Error(err, "unable to create storage calculator cronjob", "controller", "StorageCalculator")
		os.Exit(1)
	}
	// start crons.
	c.Start()
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
