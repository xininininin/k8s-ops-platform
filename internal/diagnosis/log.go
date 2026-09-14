package diagnosis

import (
	"encoding/json"
	"log/slog"
)

type LogSink struct{ Logger *slog.Logger }

func (s LogSink) Handle(result DiagnosisResult) {
	evidence, _ := json.Marshal(result.Evidence)
	s.Logger.Warn("diagnosis",
		"resource_kind", result.ResourceKind,
		"namespace", result.Namespace,
		"name", result.Name,
		"reason", result.Reason,
		"severity", result.Severity,
		"summary", result.Summary,
		"suggested_action", result.SuggestedAction,
		"evidence", string(evidence),
		"timestamp", result.Timestamp,
	)
}
