package action

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
)

type PodRestartExecutor struct{ Client client.Client }

func (e *PodRestartExecutor) Prepare(ctx context.Context, action *api.Action) error {
	if action.Status.TargetUID != "" {
		return nil
	}
	pod := &corev1.Pod{}
	key := types.NamespacedName{Namespace: action.Spec.Target.Namespace, Name: action.Spec.Target.Name}
	if err := e.Client.Get(ctx, key, pod); err != nil {
		return fmt.Errorf("get target pod: %w", err)
	}
	action.Status.TargetUID = string(pod.UID)
	for _, owner := range pod.OwnerReferences {
		if owner.Controller != nil && *owner.Controller {
			action.Status.WorkloadKind = owner.Kind
			action.Status.WorkloadName = owner.Name
			break
		}
	}
	if action.Status.WorkloadName == "" {
		return fmt.Errorf("pod %s/%s has no controller owner; restart recovery cannot be verified", pod.Namespace, pod.Name)
	}
	return nil
}

func (e *PodRestartExecutor) Execute(ctx context.Context, action *api.Action) error {
	pod := &corev1.Pod{}
	key := types.NamespacedName{Namespace: action.Spec.Target.Namespace, Name: action.Spec.Target.Name}
	if err := e.Client.Get(ctx, key, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if string(pod.UID) != action.Status.TargetUID {
		return nil
	}
	return e.Client.Delete(ctx, pod)
}

func (e *PodRestartExecutor) Verify(ctx context.Context, action *api.Action) (bool, error) {
	pods := &corev1.PodList{}
	if err := e.Client.List(ctx, pods, client.InNamespace(action.Spec.Target.Namespace)); err != nil {
		return false, err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if string(pod.UID) == action.Status.TargetUID || !ownedBy(pod.OwnerReferences, action.Status.WorkloadKind, action.Status.WorkloadName) {
			continue
		}
		if podReady(pod) {
			return true, nil
		}
	}
	return false, nil
}

func ownedBy(refs []metav1.OwnerReference, kind, name string) bool {
	for _, ref := range refs {
		if ref.Kind == kind && ref.Name == name && ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	return false
}

func podReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
