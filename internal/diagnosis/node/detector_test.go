package node

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNodeConditions(t *testing.T) {
	tests := []struct {
		name, reason string
		condition    corev1.NodeCondition
	}{
		{"not ready", "NotReady", corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionFalse}},
		{"memory pressure", "MemoryPressure", corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{tt.condition}}}
			got := NewDetector().Diagnose(n)
			if len(got) != 1 || got[0].Reason != tt.reason || len(got[0].Evidence) < 2 {
				t.Fatalf("unexpected diagnosis: %#v", got)
			}
		})
	}
}
