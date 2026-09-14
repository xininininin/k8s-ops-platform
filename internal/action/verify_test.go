package action

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
)

func TestPodRestartVerifyRequiresNewReadyOwnedPod(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "replacement", Namespace: "default", UID: "new", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-rs", Controller: &controller}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	executor := &PodRestartExecutor{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()}
	a := &api.Action{Spec: api.ActionSpec{Target: api.TargetReference{Namespace: "default"}}, Status: api.ActionStatus{TargetUID: "old", WorkloadKind: "ReplicaSet", WorkloadName: "web-rs"}}
	ok, err := executor.Verify(context.Background(), a)
	if err != nil || !ok {
		t.Fatalf("verify=%v err=%v", ok, err)
	}
	pod.Status.Conditions[0].Status = corev1.ConditionFalse
	executor.Client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	ok, err = executor.Verify(context.Background(), a)
	if err != nil || ok {
		t.Fatalf("not-ready verify=%v err=%v", ok, err)
	}
}

func TestDeploymentRestartVerifyRolloutState(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	replicas := int32(2)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default", Generation: 4}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{restartAnnotation: "action-1"}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 4, UpdatedReplicas: 2, AvailableReplicas: 2}}
	executor := &DeploymentRestartExecutor{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(d).Build()}
	a := &api.Action{ObjectMeta: metav1.ObjectMeta{UID: types.UID("action-1")}, Spec: api.ActionSpec{Target: api.TargetReference{Namespace: "default", Name: "web"}}}
	ok, err := executor.Verify(context.Background(), a)
	if err != nil || !ok {
		t.Fatalf("verify=%v err=%v", ok, err)
	}
}
