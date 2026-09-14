package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
	"github.com/example/k8s-ops-platform/internal/diagnosis"
	deploymentdiagnosis "github.com/example/k8s-ops-platform/internal/diagnosis/deployment"
	nodediagnosis "github.com/example/k8s-ops-platform/internal/diagnosis/node"
	poddiagnosis "github.com/example/k8s-ops-platform/internal/diagnosis/pod"
	"github.com/example/k8s-ops-platform/internal/kube"
	metricsinternal "github.com/example/k8s-ops-platform/internal/metrics"
	"github.com/example/k8s-ops-platform/internal/policy"
)

// cacheSource describes one SharedInformer store so the agent can expose exactly
// what it holds locally. This is deliberately observable state: it is the proof
// that List-Watch delivered real cluster objects, and it stays meaningful even
// when no diagnosis rule matches.
type cacheSource struct {
	Kind      string
	Store     cache.Store
	HasSynced cache.InformerSynced
}

func main() {
	kubeconfig := flag.String("kubeconfig-path", "", "path to kubeconfig; defaults to in-cluster or ~/.kube/config")
	resync := flag.Duration("resync", 10*time.Minute, "informer safety resync period (not polling)")
	autoPodRestart := flag.Bool("auto-pod-restart", false, "allow low-risk PodRestart Action creation")
	autoDeploymentRestart := flag.Bool("auto-deployment-restart", false, "allow medium-risk DeploymentRestart Action creation")
	metricsAddress := flag.String("metrics-bind-address", ":8080", "metrics listen address")
	cacheRefresh := flag.Duration("cache-metrics-interval", 30*time.Second, "how often informer cache object counts are published")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config, err := kube.Config(*kubeconfig)
	if err != nil {
		logger.Error("build kubernetes config", "error", err)
		os.Exit(1)
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.Error("create kubernetes client", "error", err)
		os.Exit(1)
	}
	actionScheme := runtime.NewScheme()
	_ = api.AddToScheme(actionScheme)
	actionClient, err := client.New(config, client.Options{Scheme: actionScheme})
	if err != nil {
		logger.Error("create action client", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// All four informers are constructed before Start so a single shared factory
	// owns their List-Watch loops.
	factory := informers.NewSharedInformerFactory(kubeClient, *resync)
	pods := factory.Core().V1().Pods()
	nodes := factory.Core().V1().Nodes()
	deployments := factory.Apps().V1().Deployments()
	events := factory.Core().V1().Events()

	sink := diagnosis.MultiSink{
		diagnosis.LogSink{Logger: logger},
		&policy.Sink{Client: actionClient, Logger: logger, Config: policy.Config{AutoPodRestart: *autoPodRestart, AutoDeploymentRestart: *autoDeploymentRestart, ActionTimeoutSeconds: 300, ExcludedNamespaces: []string{"k8s-ops-platform", "kube-system"}}},
	}
	podDetector := poddiagnosis.NewDetector()
	nodeDetector := nodediagnosis.NewDetector()
	deploymentDetector := deploymentdiagnosis.NewDetector()
	diagnosisMetrics := metricsinternal.NewDiagnosisTracker()

	diagnosePod := func(obj interface{}) {
		pod, ok := object[*corev1.Pod](obj)
		if !ok {
			return
		}
		namespaceEvents, err := events.Lister().Events(pod.Namespace).List(labels.Everything())
		if err != nil {
			logger.Error("list cached events", "namespace", pod.Namespace, "error", err)
			return
		}
		results := podDetector.Diagnose(pod, namespaceEvents)
		diagnosisMetrics.ObserveResource("Pod", pod.Namespace, pod.Name, results)
		for _, result := range results {
			sink.Handle(result)
		}
	}
	diagnoseNode := func(obj interface{}) {
		node, ok := object[*corev1.Node](obj)
		if !ok {
			return
		}
		results := nodeDetector.Diagnose(node)
		diagnosisMetrics.ObserveResource("Node", "", node.Name, results)
		for _, result := range results {
			sink.Handle(result)
		}
	}
	diagnoseDeployment := func(obj interface{}) {
		deployment, ok := object[*appsv1.Deployment](obj)
		if !ok {
			return
		}
		results := deploymentDetector.Diagnose(deployment)
		diagnosisMetrics.ObserveResource("Deployment", deployment.Namespace, deployment.Name, results)
		for _, result := range results {
			sink.Handle(result)
		}
	}

	_, _ = pods.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    diagnosePod,
		UpdateFunc: func(_, current interface{}) { diagnosePod(current) },
		DeleteFunc: func(obj interface{}) {
			if pod, ok := object[*corev1.Pod](obj); ok {
				diagnosisMetrics.ObserveResource("Pod", pod.Namespace, pod.Name, nil)
			}
		},
	})
	_, _ = nodes.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    diagnoseNode,
		UpdateFunc: func(_, current interface{}) { diagnoseNode(current) },
		DeleteFunc: func(obj interface{}) {
			if node, ok := object[*corev1.Node](obj); ok {
				diagnosisMetrics.ObserveResource("Node", "", node.Name, nil)
			}
		},
	})
	_, _ = deployments.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    diagnoseDeployment,
		UpdateFunc: func(_, current interface{}) { diagnoseDeployment(current) },
		DeleteFunc: func(obj interface{}) {
			if deployment, ok := object[*appsv1.Deployment](obj); ok {
				diagnosisMetrics.ObserveResource("Deployment", deployment.Namespace, deployment.Name, nil)
			}
		},
	})
	_, _ = events.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{AddFunc: func(obj interface{}) {
		event, ok := object[*corev1.Event](obj)
		if !ok || event.InvolvedObject.Kind != "Pod" {
			return
		}
		if pod, err := pods.Lister().Pods(event.Namespace).Get(event.InvolvedObject.Name); err == nil {
			diagnosePod(pod)
		}
	}})

	cacheSources := []cacheSource{
		{Kind: "Node", Store: nodes.Informer().GetStore(), HasSynced: nodes.Informer().HasSynced},
		{Kind: "Pod", Store: pods.Informer().GetStore(), HasSynced: pods.Informer().HasSynced},
		{Kind: "Deployment", Store: deployments.Informer().GetStore(), HasSynced: deployments.Informer().HasSynced},
		{Kind: "Event", Store: events.Informer().GetStore(), HasSynced: events.Informer().HasSynced},
	}
	publishCacheObjects := func() {
		for _, source := range cacheSources {
			metricsinternal.SetInformerCacheObjects(source.Kind, len(source.Store.List()))
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// /state is the agent's own answer to "what cluster state have you actually
	// received?", so cache contents can be verified without kubectl.
	mux.HandleFunc("/state", func(w http.ResponseWriter, _ *http.Request) {
		const maxNames = 25
		type resourceState struct {
			Kind      string   `json:"kind"`
			Objects   int      `json:"objects"`
			HasSynced bool     `json:"hasSynced"`
			Names     []string `json:"names,omitempty"`
			Truncated bool     `json:"truncated,omitempty"`
		}
		state := struct {
			Resources []resourceState `json:"resources"`
		}{}
		for _, source := range cacheSources {
			objects := source.Store.List()
			names := make([]string, 0, min(len(objects), maxNames))
			for _, obj := range objects {
				if len(names) == maxNames {
					break
				}
				key, err := cache.MetaNamespaceKeyFunc(obj)
				if err != nil {
					continue
				}
				names = append(names, key)
			}
			sort.Strings(names)
			state.Resources = append(state.Resources, resourceState{
				Kind:      source.Kind,
				Objects:   len(objects),
				HasSynced: source.HasSynced(),
				Names:     names,
				Truncated: len(objects) > len(names),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(state); err != nil {
			logger.Error("encode agent state", "error", err)
		}
	})
	server := &http.Server{Addr: *metricsAddress, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server", "error", err)
			stop()
		}
	}()
	defer func() { _ = server.Shutdown(context.Background()) }()

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), pods.Informer().HasSynced, nodes.Informer().HasSynced, deployments.Informer().HasSynced, events.Informer().HasSynced) {
		logger.Error("informer cache sync failed")
		os.Exit(1)
	}
	publishCacheObjects()
	go func() {
		ticker := time.NewTicker(*cacheRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				publishCacheObjects()
			}
		}
	}()
	logger.Info("diagnosis agent started", "resources", []string{"Node", "Pod", "Deployment", "Event"})
	<-ctx.Done()
}

func object[T any](obj interface{}) (T, bool) {
	value, ok := obj.(T)
	if ok {
		return value, true
	}
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		value, ok = tombstone.Obj.(T)
		return value, ok
	}
	var zero T
	return zero, false
}
