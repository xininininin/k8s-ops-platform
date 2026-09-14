package audit

import (
	"log/slog"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
)

type Recorder interface {
	Record(*api.Action, string, error)
}

type LogRecorder struct{ Logger *slog.Logger }

func (r LogRecorder) Record(action *api.Action, result string, err error) {
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	errorMessage := ""
	if err != nil {
		errorMessage = err.Error()
	}
	logger.Info("action_audit",
		"action_uid", action.UID,
		"action_type", action.Spec.ActionType,
		"target_kind", action.Spec.Target.Kind,
		"target_namespace", action.Spec.Target.Namespace,
		"target_name", action.Spec.Target.Name,
		"trigger_source", action.Spec.TriggerSource,
		"start_time", action.Status.StartedAt,
		"result", result,
		"retry_count", action.Status.RetryCount,
		"error", errorMessage,
	)
}
