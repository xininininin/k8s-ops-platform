package controllers

import (
	"testing"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
)

func TestStateTransitions(t *testing.T) {
	phase := api.ActionPhase("")
	want := []api.ActionPhase{api.ActionPhasePending, api.ActionPhaseRunning, api.ActionPhaseVerifying, api.ActionPhaseSucceeded}
	for _, expected := range want {
		next, ok := NextPhase(phase)
		if !ok || next != expected {
			t.Fatalf("from %q got %q, ok=%v; want %q", phase, next, ok, expected)
		}
		phase = next
	}
	if _, ok := NextPhase(phase); ok || !Terminal(phase) {
		t.Fatal("Succeeded must be terminal")
	}
	if !Terminal(api.ActionPhaseFailed) {
		t.Fatal("Failed must be terminal")
	}
}
