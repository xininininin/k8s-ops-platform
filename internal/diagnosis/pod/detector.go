package pod

import (
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/example/k8s-ops-platform/internal/diagnosis"
)

type Detector struct{ Now func() time.Time }

func NewDetector() *Detector { return &Detector{Now: time.Now} }

func (d *Detector) Diagnose(pod *corev1.Pod, events []*corev1.Event) []diagnosis.DiagnosisResult {
	var results []diagnosis.DiagnosisResult
	if pod.Status.Phase == corev1.PodPending {
		for _, event := range events {
			if event.InvolvedObject.UID == pod.UID && event.Reason == "FailedScheduling" && event.Type == corev1.EventTypeWarning {
				results = append(results, d.result(pod, "FailedScheduling", diagnosis.SeverityWarning,
					"Pod cannot be scheduled", "", []diagnosis.Evidence{
						{Source: "PodStatus", Field: "status.phase", Value: string(pod.Status.Phase)},
						{Source: "Event", Field: "reason", Value: event.Reason, Message: event.Message, Timestamp: eventTime(event)},
					}))
				break
			}
		}
	}

	statuses := append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	seen := map[string]bool{}
	for _, status := range statuses {
		base := []diagnosis.Evidence{
			{Source: "PodStatus", Field: "status.phase", Value: string(pod.Status.Phase)},
			{Source: "ContainerStatus", Field: "name", Value: status.Name},
			{Source: "ContainerStatus", Field: "restartCount", Value: strconv.FormatInt(int64(status.RestartCount), 10)},
		}
		if status.State.Waiting != nil {
			reason := status.State.Waiting.Reason
			switch reason {
			case "CrashLoopBackOff":
				if !seen[reason] && status.RestartCount > 0 {
					evidence := append(base, diagnosis.Evidence{Source: "ContainerState", Field: "waiting.reason", Value: reason, Message: status.State.Waiting.Message})
					if status.LastTerminationState.Terminated != nil {
						evidence = append(evidence, terminationEvidence(status.LastTerminationState.Terminated)...)
					}
					results = append(results, d.result(pod, reason, diagnosis.SeverityCritical, "Container repeatedly crashes", "PodRestart", evidence))
					seen[reason] = true
				}
			case "ImagePullBackOff", "ErrImagePull":
				canonical := "ImagePullBackOff"
				if !seen[canonical] {
					evidence := append(base, diagnosis.Evidence{Source: "ContainerState", Field: "waiting.reason", Value: reason, Message: status.State.Waiting.Message})
					results = append(results, d.result(pod, canonical, diagnosis.SeverityWarning, "Container image cannot be pulled", "", evidence))
					seen[canonical] = true
				}
			}
		}
		if terminated := status.LastTerminationState.Terminated; terminated != nil && terminated.Reason == "OOMKilled" && !seen["OOMKilled"] {
			evidence := append(base, terminationEvidence(terminated)...)
			results = append(results, d.result(pod, "OOMKilled", diagnosis.SeverityCritical, "Container was terminated by the OOM killer", "PodRestart", evidence))
			seen["OOMKilled"] = true
		}
	}
	return results
}

func (d *Detector) result(pod *corev1.Pod, reason, severity, summary, action string, evidence []diagnosis.Evidence) diagnosis.DiagnosisResult {
	return diagnosis.DiagnosisResult{ResourceKind: "Pod", Namespace: pod.Namespace, Name: pod.Name,
		Reason: reason, Severity: severity, Summary: summary, Evidence: evidence,
		SuggestedAction: action, Timestamp: d.Now()}
}

func terminationEvidence(t *corev1.ContainerStateTerminated) []diagnosis.Evidence {
	return []diagnosis.Evidence{
		{Source: "LastTerminationState", Field: "reason", Value: t.Reason, Message: t.Message, Timestamp: t.FinishedAt.Time},
		{Source: "LastTerminationState", Field: "exitCode", Value: strconv.FormatInt(int64(t.ExitCode), 10)},
	}
}

func eventTime(e *corev1.Event) time.Time {
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	return e.CreationTimestamp.Time
}

func ResourceKey(pod *corev1.Pod) string { return fmt.Sprintf("%s/%s", pod.Namespace, pod.Name) }
