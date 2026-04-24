/*
Copyright 2025 The RBG Authors.

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
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	portallocator "sigs.k8s.io/rbgs/pkg/port-allocator"
	schev1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
	volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	rawzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadscontroller "sigs.k8s.io/rbgs/internal/controller/workloads"
	"sigs.k8s.io/rbgs/pkg/scheduler"
	"sigs.k8s.io/rbgs/pkg/utils/fieldindex"
	rbgwebhook "sigs.k8s.io/rbgs/pkg/webhook"
	"sigs.k8s.io/rbgs/version"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

// WebhookMode constants control which webhooks are enabled.
// Currently supported values: "all", "none".
// This may be extended in the future to allow enabling individual webhook
// types (e.g. "admission", "conversion", "validating") for fine-grained debugging.
const (
	WebhookModeAll  = "all"
	WebhookModeNone = "none"
)

// validateWebhookMode checks if the webhook mode is a valid value.
func validateWebhookMode(mode string) error {
	switch mode {
	case WebhookModeAll, WebhookModeNone:
		return nil
	default:
		return fmt.Errorf("invalid value for --enable-webhooks %q: supported values are %q and %q", mode, WebhookModeAll, WebhookModeNone)
	}
}

// webhooksEnabled returns true if the given webhook mode enables webhooks.
func webhooksEnabled(mode string) bool {
	return mode == WebhookModeAll
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextv1.AddToScheme(scheme))
	utilruntime.Must(lwsv1.AddToScheme(scheme))
	utilruntime.Must(schev1alpha1.AddToScheme(scheme))
	utilruntime.Must(volcanoschedulingv1beta1.AddToScheme(scheme))

	utilruntime.Must(workloadsv1alpha2.AddToScheme(scheme))
	utilruntime.Must(workloadsv1alpha1.AddToScheme(scheme))
	utilruntime.Must(workloadsv1alpha2.AddToScheme(clientgoscheme.Scheme))
	// +kubebuilder:scaffold:scheme
}

func printVersion() {
	setupLog.Info(
		fmt.Sprintf(
			"RoleBasedGroup Controller Version: %s, git commit: %s, build date: %s",
			version.Version, version.GitCommit, version.BuildDate,
		),
	)
	setupLog.Info(fmt.Sprintf("Go Version: %s", goruntime.Version()))
	setupLog.Info(fmt.Sprintf("Go OS/Arch: %s/%s", goruntime.GOOS, goruntime.GOARCH))
}

// nolint:gocyclo
func main() {
	var (
		metricsAddr                                      string
		metricsCertPath, metricsCertName, metricsCertKey string
		enableLeaderElection                             bool
		probeAddr                                        string
		secureMetrics                                    bool
		enableHTTP2                                      bool
		tlsOpts                                          []func(*tls.Config)
		development                                      bool
		webhookMode                                      string
		// Controller runtime options
		maxConcurrentReconciles int
		cacheSyncTimeout        time.Duration
		portAllocateStrategy    string
		startPort               int
		portRange               int
		enablePortAllocator     bool
		// Gang scheduling scheduler name: scheduler-plugins or volcano
		schedulerName string
	)
	flag.StringVar(
		&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
			"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.",
	)
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8082", "The address the probe endpoint binds to.")
	flag.BoolVar(
		&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.",
	)
	flag.BoolVar(
		&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.",
	)
	flag.StringVar(
		&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.",
	)
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(
		&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers",
	)
	flag.BoolVar(&development, "development", false, "Enable development mode for controller manager.")
	flag.StringVar(
		&webhookMode, "enable-webhooks", WebhookModeAll,
		"Control which webhooks are enabled. Supported values: "+WebhookModeAll+", "+WebhookModeNone+". "+
			"Use "+WebhookModeNone+" to disable all webhooks (cert bootstrap, conversion, admission) for local debugging only. "+
			"When set to "+WebhookModeNone+", leader election is also disabled and metrics are served insecurely over HTTP.",
	)
	flag.IntVar(
		&maxConcurrentReconciles, "max-concurrent-reconciles", 10,
		"The number of worker threads used by the the RBGS controller.",
	)
	flag.DurationVar(&cacheSyncTimeout, "cache-sync-timeout", 120*time.Second, "Informer cache sync timeout.")
	flag.BoolVar(&enablePortAllocator, "enable-port-allocator", false, "Enable the port allocator.")
	flag.StringVar(&portAllocateStrategy, "port-allocate-strategy", "random", "The strategy to allocate ports.")
	flag.IntVar(&startPort, "start-port", 30000, "The start port to allocate.")
	flag.IntVar(&portRange, "port-range", 5000, "The range of ports to allocate.")
	flag.StringVar(
		&schedulerName, "scheduler-name", string(scheduler.KubeSchedulerPlugin),
		"The scheduler name to use for gang scheduling. Supported values: scheduler-plugins, volcano. "+
			"Defaults to scheduler-plugins.",
	)
	flag.Parse()

	// Validate webhook mode to prevent typos silently disabling webhooks.
	if err := validateWebhookMode(webhookMode); err != nil {
		setupLog.Error(err, "invalid --enable-webhooks value")
		os.Exit(1)
	}

	opts := zap.Options{
		Development: development,
		EncoderConfigOptions: []zap.EncoderConfigOption{
			func(ec *zapcore.EncoderConfig) {
				ec.MessageKey = "message"
				ec.LevelKey = "level"
				ec.TimeKey = "time"
				ec.CallerKey = "caller"
				ec.EncodeLevel = zapcore.CapitalLevelEncoder
				ec.EncodeCaller = zapcore.ShortCallerEncoder
				ec.EncodeTime = zapcore.ISO8601TimeEncoder
			},
		},
		ZapOpts: []rawzap.Option{
			rawzap.AddCaller(),
		},
	}
	opts.BindFlags(flag.CommandLine)

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	printVersion()

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Create watcher for metrics certificates
	var metricsCertWatcher *certwatcher.CertWatcher

	// Webhook TLS options
	webhookTLSOpts := tlsOpts

	webhookServer := newWebhookServer(webhookMode, webhookTLSOpts)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info(
			"Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key",
			metricsCertKey,
		)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(metricsCertPath, metricsCertName),
			filepath.Join(metricsCertPath, metricsCertKey),
		)
		if err != nil {
			setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
			os.Exit(1)
		}

		metricsServerOptions.TLSOpts = append(
			metricsServerOptions.TLSOpts, func(config *tls.Config) {
				config.GetCertificate = metricsCertWatcher.GetCertificate
			},
		)
	}

	mgr, err := ctrl.NewManager(
		ctrl.GetConfigOrDie(), newManagerOptions(webhookMode, webhookServer, metricsServerOptions, probeAddr, enableLeaderElection),
	)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	options := controller.Options{
		MaxConcurrentReconciles: maxConcurrentReconciles,
		CacheSyncTimeout:        cacheSyncTimeout,
	}

	// ---------------------------------------------------------------------------
	// Self-signed TLS certificate bootstrap for the conversion webhook.
	// Generates (or loads) a cert stored in a Secret, writes it to WebhookCertDir
	// so the webhook server can serve HTTPS, and patches the caBundle on the CRDs.
	// Skipped when webhooks are disabled.
	// ---------------------------------------------------------------------------
	var webhookResult *webhookBootstrapResult
	if webhooksEnabled(webhookMode) {
		webhookResult, err = bootstrapWebhookCerts(mgr)
		if err != nil {
			setupLog.Error(err, "unable to bootstrap webhook certs")
			os.Exit(1)
		}
	} else {
		setupLog.Info("Webhooks are disabled: cert bootstrap, conversion and admission webhooks will not be started")
	}

	rbgReconciler, err := workloadscontroller.NewRoleBasedGroupReconciler(mgr, scheduler.SchedulerPluginType(schedulerName))
	if err != nil {
		setupLog.Error(err, "unable to create rbg controller", "controller", "RoleBasedGroup")
		os.Exit(1)
	}
	if err = rbgReconciler.CheckCrdExists(); err != nil {
		setupLog.Error(err, "unable to create rbg controller", "controller", "RoleBasedGroup")
		os.Exit(1)
	}

	if err = rbgReconciler.SetupWithManager(mgr, options); err != nil {
		setupLog.Error(err, "unable to create rbg controller", "controller", "RoleBasedGroup")
		os.Exit(1)
	}

	podReconciler := workloadscontroller.NewPodReconciler(mgr)
	if err = podReconciler.SetupWithManager(mgr, options); err != nil {
		setupLog.Error(err, "unable to create pod controller", "controller", "Pod")
		os.Exit(1)
	}

	rbgScalingAdapterReconciler := workloadscontroller.NewRoleBasedGroupScalingAdapterReconciler(mgr)
	if err = rbgScalingAdapterReconciler.CheckCrdExists(); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "RoleBasedGroupScalingAdapter")
		os.Exit(1)
	}
	if err = rbgScalingAdapterReconciler.SetupWithManager(mgr, options); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "RoleBasedGroupScalingAdapter")
		os.Exit(1)
	}

	rbgsReconciler := workloadscontroller.NewRoleBasedGroupSetReconciler(mgr)
	if err = rbgsReconciler.CheckCrdExists(); err != nil {
		setupLog.Error(err, "unable to create rbgs controller", "controller", "RoleBasedGroupSet")
		os.Exit(1)
	}

	if err = rbgsReconciler.SetupWithManager(mgr, options); err != nil {
		setupLog.Error(err, "unable to create rbgs controller", "controller", "RoleBasedGroupSet")
		os.Exit(1)
	}

	roleInstanceReconciler := workloadscontroller.NewRoleInstanceReconciler(mgr)
	if err = roleInstanceReconciler.CheckCrdExists(); err != nil {
		setupLog.Error(err, "unable to create roleinstance controller", "controller", "RoleInstance")
		os.Exit(1)
	}

	if err = roleInstanceReconciler.SetupWithManager(mgr, options); err != nil {
		setupLog.Error(err, "unable to create roleinstance controller", "controller", "RoleInstance")
		os.Exit(1)
	}

	roleInstanceSetReconciler := workloadscontroller.NewRoleInstanceSetReconciler(mgr)
	if err = roleInstanceSetReconciler.CheckCrdExists(); err != nil {
		setupLog.Error(err, "unable to create roleinstanceset controller", "controller", "RoleInstanceSet")
		os.Exit(1)
	}

	if err = roleInstanceSetReconciler.SetupWithManager(mgr, options); err != nil {
		setupLog.Error(err, "unable to create roleinstanceset controller", "controller", "RoleInstanceSet")
		os.Exit(1)
	}

	setupLog.Info("register field index")
	if err = fieldindex.RegisterFieldIndexes(mgr.GetCache()); err != nil {
		setupLog.Error(err, "failed to register field index")
		os.Exit(1)
	}

	// Webhook cert controller: watches the conversion-webhook CRDs and keeps
	// caBundle in sync with the self-signed CA certificate.
	// Skipped when webhooks are disabled.
	if webhooksEnabled(webhookMode) {
		if err = setupWebhookCertController(mgr, webhookResult, options); err != nil {
			setupLog.Error(err, "unable to create webhook cert controller")
			os.Exit(1)
		}
	}

	// +kubebuilder:scaffold:builder
	if metricsCertWatcher != nil {
		setupLog.Info("Adding metrics certificate watcher to manager")
		if err := mgr.Add(metricsCertWatcher); err != nil {
			setupLog.Error(err, "unable to add metrics certificate watcher to manager")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	if err := portallocator.SetupPortAllocator(startPort, portRange, portAllocateStrategy, enablePortAllocator, mgr.GetClient()); err != nil {
		setupLog.Error(err, "unable to initialize port allocator")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// webhookBootstrapResult holds the result of webhook cert bootstrap.
type webhookBootstrapResult struct {
	certMgr *rbgwebhook.CertManager
	caCert  []byte
}

// newWebhookServer creates a webhook server with the given TLS options.
// Returns nil when webhooks are disabled (enable-webhooks=none).
func newWebhookServer(webhookMode string, tlsOpts []func(*tls.Config)) webhook.Server {
	if !webhooksEnabled(webhookMode) {
		return nil
	}
	return webhook.NewServer(webhook.Options{
		CertDir: rbgwebhook.WebhookCertDir,
		TLSOpts: tlsOpts,
	})
}

// newManagerOptions builds the controller-runtime manager options.
// When webhooks are disabled (enable-webhooks=none), webhook server and leader
// election are disabled, and metrics are served insecurely.
func newManagerOptions(webhookMode string, webhookServer webhook.Server, metricsOpts metricsserver.Options, probeAddr string, enableLeaderElection bool) ctrl.Options {
	opts := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsOpts,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       constants.ControllerName,
		Cache:                  cacheOptions(),
	}
	if !webhooksEnabled(webhookMode) {
		setupLog.Info("Webhooks disabled: forcing LeaderElection=false, Metrics.SecureServing=false")
		opts.WebhookServer = nil
		opts.LeaderElection = false
		opts.Metrics.SecureServing = false
		opts.Metrics.FilterProvider = nil
	}
	return opts
}

// bootstrapWebhookCerts bootstraps the self-signed TLS certificate for the
// conversion webhook, patches the caBundle on CRDs, and registers conversion
// webhooks with the manager. This should only be called when webhook is enabled.
func bootstrapWebhookCerts(mgr ctrl.Manager) (*webhookBootstrapResult, error) {
	webhookServiceNamespace := os.Getenv("POD_NAMESPACE")
	if webhookServiceNamespace == "" {
		setupLog.Info("WARNING: POD_NAMESPACE env not found; caBundle patching may fail")
	}

	// Use a direct (non-cached) client for cert bootstrap: mgr.GetClient() uses
	// the informer cache which is not started until mgr.Start(), so it cannot
	// serve reads at this point in startup.
	directClient, err := client.New(mgr.GetConfig(), client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("unable to create direct client for cert bootstrap: %w", err)
	}

	certMgr, err := rbgwebhook.NewCertManager(directClient, rbgwebhook.WebhookCertSecretName, webhookServiceNamespace)
	if err != nil {
		return nil, fmt.Errorf("unable to create webhook cert manager: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	caCert, err := certMgr.BuildOrSync(ctx, webhookServiceNamespace, rbgwebhook.WebhookServiceName, rbgwebhook.WebhookCertDir)
	if err != nil {
		return nil, fmt.Errorf("unable to provision webhook TLS certificate: %w", err)
	}

	if err = certMgr.PatchCRDCABundle(ctx, rbgwebhook.ConversionWebhookCRDs(), caCert); err != nil {
		return nil, fmt.Errorf("unable to patch caBundle on conversion CRDs: %w", err)
	}

	// Register conversion webhooks so the API server can convert between v1alpha1 and v1alpha2.
	if err = (&workloadsv1alpha2.RoleBasedGroup{}).SetupWebhookWithManager(mgr); err != nil {
		return nil, fmt.Errorf("unable to create conversion webhook for RoleBasedGroup: %w", err)
	}
	if err = (&workloadsv1alpha2.RoleBasedGroupSet{}).SetupWebhookWithManager(mgr); err != nil {
		return nil, fmt.Errorf("unable to create conversion webhook for RoleBasedGroupSet: %w", err)
	}

	return &webhookBootstrapResult{certMgr: certMgr, caCert: caCert}, nil
}

// setupWebhookCertController sets up the webhook cert controller that watches
// the conversion-webhook CRDs and keeps caBundle in sync with the self-signed CA certificate.
func setupWebhookCertController(mgr ctrl.Manager, result *webhookBootstrapResult, options controller.Options) error {
	webhookCertReconciler := &workloadscontroller.WebhookCertReconciler{
		Client:      mgr.GetClient(),
		CertManager: result.certMgr,
		CACert:      result.caCert,
		CRDNames:    rbgwebhook.ConversionWebhookCRDs(),
	}
	return webhookCertReconciler.SetupWithManager(mgr, options)
}

func cacheOptions() cache.Options {
	keyExistsRequirement, err := labels.NewRequirement(constants.GroupNameLabelKey, selection.Exists, nil)
	if err != nil {
		panic(err)
	}
	keyExistsSelector := labels.NewSelector().Add(*keyExistsRequirement)

	return cache.Options{
		Scheme: scheme,
		ByObject: map[client.Object]cache.ByObject{
			&appsv1.StatefulSet{}: {
				Label: keyExistsSelector,
			},
			&appsv1.Deployment{}: {
				Label: keyExistsSelector,
			},
			&corev1.Service{}: {
				Label: keyExistsSelector,
			},
		},
	}
}
