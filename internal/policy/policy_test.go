package policy

import (
	"testing"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
)

func TestRiskAndAutoCreate(t *testing.T) {
	config := Config{AutoPodRestart: true, AutoDeploymentRestart: false}
	tests := []struct {
		action api.ActionType
		risk   Risk
		auto   bool
	}{
		{api.ActionTypePodRestart, RiskLow, true},
		{api.ActionTypeDeploymentRestart, RiskMedium, false},
		{api.ActionTypeNodeMaintenance, RiskHigh, false},
	}
	for _, tt := range tests {
		if got := RiskFor(tt.action); got != tt.risk {
			t.Errorf("RiskFor(%s)=%s want %s", tt.action, got, tt.risk)
		}
		if got := config.ShouldAutoCreate(tt.action); got != tt.auto {
			t.Errorf("ShouldAutoCreate(%s)=%v want %v", tt.action, got, tt.auto)
		}
	}
}
