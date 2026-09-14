package deployment

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/example/k8s-ops-platform/internal/diagnosis"
)

func newDeployment(desired, generation, observedGeneration, available, ready, unavailable int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "sre-agent-test", Generation: int64(generation)},
		Spec:       appsv1.DeploymentSpec{Replicas: &desired},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration:  int64(observedGeneration),
			AvailableReplicas:   available,
			ReadyReplicas:       ready,
			UnavailableReplicas: unavailable,
		},
	}
}

func TestDiagnoseReportsUnavailableReplicas(t *testing.T) {
	detector := &Detector{Now: func() time.Time { return time.Unix(0, 0) }}
	deployment := newDeployment(2, 2, 2, 1, 1, 1)
	deployment.Status.Conditions = []appsv1.DeploymentCondition{{
		Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue,
		Reason: "ReplicaSetUpdated", Message: "ReplicaSet \"demo-abc\" is progressing.",
	}}

	results := detector.Diagnose(deployment)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	result := results[0]
	if result.Reason != ReasonReplicasUnavailable {
		t.Errorf("reason = %q, want %q", result.Reason, ReasonReplicasUnavailable)
	}
	if result.ResourceKind != "Deployment" || result.Namespace != "sre-agent-test" || result.Name != "demo" {
		t.Errorf("unexpected target: %+v", result)
	}
	if result.Severity != diagnosis.SeverityWarning {
		t.Errorf("severity = %q, want %q", result.Severity, diagnosis.SeverityWarning)
	}
	if result.SuggestedAction != "" {
		t.Errorf("suggestedAction = %q, want empty", result.SuggestedAction)
	}
	assertEvidence(t, result, "DeploymentSpec", "spec.replicas", "2")
	assertEvidence(t, result, "DeploymentStatus", "status.availableReplicas", "1")
	assertEvidence(t, result, "DeploymentCondition", "status.conditions[Progressing].status", "True")
}

func TestDiagnoseSeverityCriticalWhenNothingAvailable(t *testing.T) {
	detector := &Detector{Now: time.Now}
	results := detector.Diagnose(newDeployment(3, 1, 1, 0, 0, 3))
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Severity != diagnosis.SeverityCritical {
		t.Errorf("severity = %q, want %q", results[0].Severity, diagnosis.SeverityCritical)
	}
}

func TestDiagnoseSilentWhenHealthy(t *testing.T) {
	detector := &Detector{Now: time.Now}
	if results := detector.Diagnose(newDeployment(2, 1, 1, 2, 2, 0)); len(results) != 0 {
		t.Fatalf("expected no results for a healthy deployment, got %+v", results)
	}
}

func TestDiagnoseSilentWhenStatusIsStale(t *testing.T) {
	detector := &Detector{Now: time.Now}
	// Generation 2 has been applied but the controller has only observed 1, so
	// the old "available" number says nothing about the current spec.
	if results := detector.Diagnose(newDeployment(2, 2, 1, 0, 0, 0)); len(results) != 0 {
		t.Fatalf("expected no results for a stale status, got %+v", results)
	}
}

func TestDiagnoseSilentWhenReplicasUnset(t *testing.T) {
	detector := &Detector{Now: time.Now}
	deployment := newDeployment(1, 1, 1, 0, 0, 0)
	deployment.Spec.Replicas = nil
	if results := detector.Diagnose(deployment); len(results) != 0 {
		t.Fatalf("expected no results when spec.replicas is unset, got %+v", results)
	}
}

func assertEvidence(t *testing.T, result diagnosis.DiagnosisResult, source, field, value string) {
	t.Helper()
	for _, evidence := range result.Evidence {
		if evidence.Source == source && evidence.Field == field {
			if evidence.Value != value {
				t.Errorf("evidence %s/%s value = %q, want %q", source, field, evidence.Value, value)
			}
			return
		}
	}
	t.Errorf("missing evidence %s/%s in %+v", source, field, result.Evidence)
}
