package metrics

import (
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
	"github.com/example/k8s-ops-platform/internal/diagnosis"
)

var (
	diagnosisTotal  = promauto.NewCounterVec(prometheus.CounterOpts{Name: "k8s_ops_diagnosis_total", Help: "Total diagnosis observations."}, []string{"resource_kind", "reason", "severity"})
	activeDiagnosis = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "k8s_ops_active_diagnosis", Help: "Current diagnosed resources."}, []string{"resource_kind", "reason", "severity"})
	actionTotal     = promauto.NewCounterVec(prometheus.CounterOpts{Name: "k8s_ops_action_total", Help: "Completed actions by result."}, []string{"action_type", "result"})
	actionFailed    = promauto.NewCounterVec(prometheus.CounterOpts{Name: "k8s_ops_action_failed_total", Help: "Failed actions."}, []string{"action_type"})
	actionDuration  = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "k8s_ops_action_duration_seconds", Help: "Action duration through verification.", Buckets: prometheus.DefBuckets}, []string{"action_type", "result"})
)

// informerCacheObjects exposes how many objects the diagnosis agent currently
// holds in its SharedInformer local cache. It is the agent-side proof that
// List-Watch actually delivered cluster state, and unlike the diagnosis
// counters it stays populated even when no diagnosis rule matches.
var informerCacheObjects = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "k8s_ops_informer_cache_objects",
	Help: "Objects currently held in the diagnosis agent SharedInformer local cache.",
}, []string{"resource_kind"})

type diagnosisLabel struct{ kind, reason, severity string }

type DiagnosisTracker struct {
	mu        sync.Mutex
	resources map[string][]diagnosisLabel
	counts    map[diagnosisLabel]int
}

func NewDiagnosisTracker() *DiagnosisTracker {
	return &DiagnosisTracker{resources: map[string][]diagnosisLabel{}, counts: map[diagnosisLabel]int{}}
}

func (t *DiagnosisTracker) ObserveResource(kind, namespace, name string, results []diagnosis.DiagnosisResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := strings.Join([]string{kind, namespace, name}, "\x00")
	for _, label := range t.resources[key] {
		t.counts[label]--
		activeDiagnosis.WithLabelValues(label.kind, label.reason, label.severity).Set(float64(t.counts[label]))
	}
	labels := make([]diagnosisLabel, 0, len(results))
	for _, result := range results {
		label := diagnosisLabel{result.ResourceKind, result.Reason, result.Severity}
		labels = append(labels, label)
		t.counts[label]++
		diagnosisTotal.WithLabelValues(label.kind, label.reason, label.severity).Inc()
		activeDiagnosis.WithLabelValues(label.kind, label.reason, label.severity).Set(float64(t.counts[label]))
	}
	t.resources[key] = labels
}

func ObserveAction(action *api.Action, result string, duration time.Duration) {
	actionType := string(action.Spec.ActionType)
	actionTotal.WithLabelValues(actionType, result).Inc()
	if result == "failed" {
		actionFailed.WithLabelValues(actionType).Inc()
	}
	actionDuration.WithLabelValues(actionType, result).Observe(duration.Seconds())
}

// SetInformerCacheObjects publishes the current object count of one informer
// store. The agent calls it for every watched resource kind (Node, Pod,
// Deployment, Event) so cache contents are observable from Prometheus.
func SetInformerCacheObjects(resourceKind string, count int) {
	informerCacheObjects.WithLabelValues(resourceKind).Set(float64(count))
}

func Register(registerer prometheus.Registerer) error {
	collectors := []prometheus.Collector{diagnosisTotal, activeDiagnosis, actionTotal, actionFailed, actionDuration, informerCacheObjects}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			if _, alreadyRegistered := err.(prometheus.AlreadyRegisteredError); !alreadyRegistered {
				return err
			}
		}
	}
	return nil
}

func Duration(action *api.Action, now time.Time) time.Duration {
	if action.Status.StartedAt == nil || now.Before(action.Status.StartedAt.Time) {
		return 0
	}
	return now.Sub(action.Status.StartedAt.Time)
}
