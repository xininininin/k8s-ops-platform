package deployment

import (
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/example/k8s-ops-platform/internal/diagnosis"
)

// ReasonReplicasUnavailable is the deployment-level symptom of a rollout that
// cannot reach its desired replica count.
const ReasonReplicasUnavailable = "DeploymentReplicasUnavailable"

type Detector struct{ Now func() time.Time }

func NewDetector() *Detector { return &Detector{Now: time.Now} }

// Diagnose compares the desired replica count with the replicas the Deployment
// controller currently reports as available. It deliberately reports only the
// Deployment-level state plus the raw status evidence; it does not guess at the
// workload-level cause, because the cause lives on the Pods (for example a
// failing readiness probe) and is correlated in a later diagnosis step.
//
// No SuggestedAction is emitted: the usual remediation for this class is not an
// automatic restart, and a wrong recommendation is worse than none.
func (d *Detector) Diagnose(deployment *appsv1.Deployment) []diagnosis.DiagnosisResult {
	if deployment.Spec.Replicas == nil {
		return nil
	}
	desired := *deployment.Spec.Replicas
	// Only judge status that the Deployment controller produced for the current
	// spec. Without this guard every spec edit looks like an outage for one cycle.
	if deployment.Status.ObservedGeneration < deployment.Generation {
		return nil
	}
	if deployment.Status.AvailableReplicas >= desired {
		return nil
	}
	severity := diagnosis.SeverityWarning
	if deployment.Status.AvailableReplicas == 0 && desired > 0 {
		severity = diagnosis.SeverityCritical
	}
	evidence := []diagnosis.Evidence{
		{Source: "DeploymentSpec", Field: "spec.replicas", Value: strconv.Itoa(int(desired))},
		{Source: "DeploymentStatus", Field: "status.availableReplicas", Value: strconv.Itoa(int(deployment.Status.AvailableReplicas))},
		{Source: "DeploymentStatus", Field: "status.readyReplicas", Value: strconv.Itoa(int(deployment.Status.ReadyReplicas))},
		{Source: "DeploymentStatus", Field: "status.unavailableReplicas", Value: strconv.Itoa(int(deployment.Status.UnavailableReplicas))},
		{Source: "DeploymentStatus", Field: "status.updatedReplicas", Value: strconv.Itoa(int(deployment.Status.UpdatedReplicas))},
	}
	for _, condition := range deployment.Status.Conditions {
		evidence = append(evidence, diagnosis.Evidence{
			Source:    "DeploymentCondition",
			Field:     fmt.Sprintf("status.conditions[%s].status", condition.Type),
			Value:     string(condition.Status),
			Message:   fmt.Sprintf("%s: %s", condition.Reason, condition.Message),
			Timestamp: condition.LastTransitionTime.Time,
		})
	}
	return []diagnosis.DiagnosisResult{{
		ResourceKind: "Deployment",
		Namespace:    deployment.Namespace,
		Name:         deployment.Name,
		Reason:       ReasonReplicasUnavailable,
		Severity:     severity,
		Summary:      fmt.Sprintf("Deployment has %d/%d available replicas", deployment.Status.AvailableReplicas, desired),
		Evidence:     evidence,
		Timestamp:    d.Now(),
	}}
}
