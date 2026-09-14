package controllers

import api "github.com/example/k8s-ops-platform/api/v1alpha1"

func NextPhase(current api.ActionPhase) (api.ActionPhase, bool) {
	switch current {
	case "":
		return api.ActionPhasePending, true
	case api.ActionPhasePending:
		return api.ActionPhaseRunning, true
	case api.ActionPhaseRunning:
		return api.ActionPhaseVerifying, true
	case api.ActionPhaseVerifying:
		return api.ActionPhaseSucceeded, true
	default:
		return current, false
	}
}

func Terminal(phase api.ActionPhase) bool {
	return phase == api.ActionPhaseSucceeded || phase == api.ActionPhaseFailed
}
