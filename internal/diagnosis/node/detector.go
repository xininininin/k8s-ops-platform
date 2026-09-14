package node

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/example/k8s-ops-platform/internal/diagnosis"
)

type Detector struct{ Now func() time.Time }

func NewDetector() *Detector { return &Detector{Now: time.Now} }

func (d *Detector) Diagnose(node *corev1.Node) []diagnosis.DiagnosisResult {
	var results []diagnosis.DiagnosisResult
	for _, condition := range node.Status.Conditions {
		switch {
		case condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue:
			results = append(results, d.result(node, "NotReady", diagnosis.SeverityCritical,
				"Node Ready condition is not True", "NodeMaintenance", condition))
		case condition.Type == corev1.NodeMemoryPressure && condition.Status == corev1.ConditionTrue:
			results = append(results, d.result(node, "MemoryPressure", diagnosis.SeverityCritical,
				"Node reports memory pressure", "NodeMaintenance", condition))
		case condition.Type == corev1.NodeDiskPressure && condition.Status == corev1.ConditionTrue:
			results = append(results, d.result(node, "DiskPressure", diagnosis.SeverityCritical,
				"Node reports disk pressure", "NodeMaintenance", condition))
		}
	}
	return results
}

func (d *Detector) result(node *corev1.Node, reason, severity, summary, action string, c corev1.NodeCondition) diagnosis.DiagnosisResult {
	return diagnosis.DiagnosisResult{
		ResourceKind: "Node", Name: node.Name, Reason: reason, Severity: severity,
		Summary: summary, SuggestedAction: action, Timestamp: d.Now(),
		Evidence: []diagnosis.Evidence{
			{Source: "NodeCondition", Field: fmt.Sprintf("status.conditions[%s].status", c.Type), Value: string(c.Status), Timestamp: c.LastTransitionTime.Time},
			{Source: "NodeCondition", Field: "reason", Value: c.Reason, Message: c.Message, Timestamp: c.LastHeartbeatTime.Time},
		},
	}
}
