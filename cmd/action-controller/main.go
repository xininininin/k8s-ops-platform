package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
	"github.com/example/k8s-ops-platform/controllers"
	actionexec "github.com/example/k8s-ops-platform/internal/action"
	"github.com/example/k8s-ops-platform/internal/kube"
	metricsinternal "github.com/example/k8s-ops-platform/internal/metrics"
)

func main() {
	kubeconfig := flag.String("kubeconfig-path", "", "path to kubeconfig; defaults to in-cluster or ~/.kube/config")
	metricsAddress := flag.String("metrics-bind-address", ":8080", "metrics listen address")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	config, err := kube.Config(*kubeconfig)
	if err != nil {
		logger.Error("build kubernetes config", "error", err)
		os.Exit(1)
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(config, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: *metricsAddress}, HealthProbeBindAddress: ":8081"})
	if err != nil {
		logger.Error("create manager", "error", err)
		os.Exit(1)
	}
	// HealthProbeBindAddress alone only starts the probe server; without a
	// registered check every path on it returns 404, so the Deployment probes
	// could report ready while the manager is unable to serve anything.
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error("register healthz check", "error", err)
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error("register readyz check", "error", err)
		os.Exit(1)
	}
	if err := metricsinternal.Register(crmetrics.Registry); err != nil {
		logger.Error("register custom metrics", "error", err)
		os.Exit(1)
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
		return []string{obj.(*corev1.Pod).Spec.NodeName}
	}); err != nil {
		logger.Error("create pod node index", "error", err)
		os.Exit(1)
	}
	typedClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.Error("create typed client", "error", err)
		os.Exit(1)
	}
	reconciler := &controllers.ActionReconciler{Client: mgr.GetClient(), Logger: logger, Executors: map[api.ActionType]actionexec.Executor{
		api.ActionTypePodRestart:        &actionexec.PodRestartExecutor{Client: mgr.GetClient()},
		api.ActionTypeDeploymentRestart: &actionexec.DeploymentRestartExecutor{Client: mgr.GetClient()},
		api.ActionTypeNodeMaintenance:   &actionexec.NodeMaintenanceExecutor{Client: mgr.GetClient(), Kube: typedClient, Hook: actionexec.NoopMaintenanceHook{}},
	}}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		logger.Error("setup controller", "error", err)
		os.Exit(1)
	}
	logger.Info("action controller starting")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error("manager stopped", "error", err)
		os.Exit(1)
	}
}
