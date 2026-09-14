package diagnosis

import "time"

type Evidence struct {
	Source    string    `json:"source"`
	Field     string    `json:"field"`
	Value     string    `json:"value"`
	Message   string    `json:"message,omitempty"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

type DiagnosisResult struct {
	ResourceKind    string     `json:"resourceKind"`
	Namespace       string     `json:"namespace,omitempty"`
	Name            string     `json:"name"`
	Reason          string     `json:"reason"`
	Severity        string     `json:"severity"`
	Summary         string     `json:"summary"`
	Evidence        []Evidence `json:"evidence"`
	SuggestedAction string     `json:"suggestedAction,omitempty"`
	Timestamp       time.Time  `json:"timestamp"`
}

const (
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

type Sink interface {
	Handle(DiagnosisResult)
}

type SinkFunc func(DiagnosisResult)

func (f SinkFunc) Handle(result DiagnosisResult) { f(result) }

type MultiSink []Sink

func (s MultiSink) Handle(result DiagnosisResult) {
	for _, sink := range s {
		if sink != nil {
			sink.Handle(result)
		}
	}
}
