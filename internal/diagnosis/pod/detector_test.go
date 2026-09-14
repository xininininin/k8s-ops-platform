package pod

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestContainerFailures(t *testing.T) {
	tests := []struct {
		name, reason string
		status       corev1.ContainerStatus
	}{
		{"crash loop", "CrashLoopBackOff", corev1.ContainerStatus{Name: "app", RestartCount: 3, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}}},
		{"image pull", "ImagePullBackOff", corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}},
		{"oom killed", "OOMKilled", corev1.ContainerStatus{Name: "app", RestartCount: 1, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "default"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{tt.status}}}
			got := NewDetector().Diagnose(p, nil)
			if len(got) != 1 || got[0].Reason != tt.reason || len(got[0].Evidence) < 3 {
				t.Fatalf("unexpected diagnosis: %#v", got)
			}
		})
	}
}

func TestFailedSchedulingRequiresPhaseAndEventIdentity(t *testing.T) {
	uid := types.UID("pod-1")
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "default", UID: uid}, Status: corev1.PodStatus{Phase: corev1.PodPending}}
	e := &corev1.Event{Type: corev1.EventTypeWarning, Reason: "FailedScheduling", InvolvedObject: corev1.ObjectReference{UID: uid}}
	got := NewDetector().Diagnose(p, []*corev1.Event{e})
	if len(got) != 1 || got[0].Reason != "FailedScheduling" {
		t.Fatalf("unexpected diagnosis: %#v", got)
	}
	e.InvolvedObject.UID = "other"
	if got := NewDetector().Diagnose(p, []*corev1.Event{e}); len(got) != 0 {
		t.Fatalf("unrelated event caused diagnosis: %#v", got)
	}
}
