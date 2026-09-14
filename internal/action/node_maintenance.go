package action

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
)

type MaintenanceHook interface {
	Run(context.Context, *corev1.Node) error
}

type NoopMaintenanceHook struct{}

func (NoopMaintenanceHook) Run(context.Context, *corev1.Node) error { return nil }

type NodeMaintenanceExecutor struct {
	Client client.Client
	Kube   kubernetes.Interface
	Hook   MaintenanceHook
}

func (e *NodeMaintenanceExecutor) Execute(ctx context.Context, action *api.Action) error {
	node := &corev1.Node{}
	if err := e.Client.Get(ctx, types.NamespacedName{Name: action.Spec.Target.Name}, node); err != nil {
		return fmt.Errorf("get node: %w", err)
	}
	switch action.Status.ExecutionState {
	case "":
		if !nodeReady(node) {
			return fmt.Errorf("precheck failed: node is not Ready")
		}
		action.Status.ExecutionState = "PreChecked"
		return InProgress("node precheck complete")
	case "PreChecked":
		if !node.Spec.Unschedulable {
			base := node.DeepCopy()
			node.Spec.Unschedulable = true
			if err := e.Client.Patch(ctx, node, client.MergeFrom(base)); err != nil {
				return fmt.Errorf("cordon node: %w", err)
			}
		}
		action.Status.ExecutionState = "Cordoned"
		return InProgress("node cordoned")
	case "Cordoned":
		remaining, err := e.evict(ctx, node.Name)
		if err != nil {
			return err
		}
		if remaining {
			return InProgress("waiting for evicted pods to terminate")
		}
		action.Status.ExecutionState = "Drained"
		return InProgress("node drained")
	case "Drained":
		if e.Hook == nil {
			e.Hook = NoopMaintenanceHook{}
		}
		if err := e.Hook.Run(ctx, node); err != nil {
			return fmt.Errorf("maintenance hook: %w", err)
		}
		action.Status.ExecutionState = "HookCompleted"
		return InProgress("maintenance hook complete")
	case "HookCompleted":
		if !nodeReady(node) {
			return InProgress("waiting for node health check")
		}
		action.Status.ExecutionState = "HealthChecked"
		return InProgress("node health check complete")
	case "HealthChecked":
		if node.Spec.Unschedulable {
			base := node.DeepCopy()
			node.Spec.Unschedulable = false
			if err := e.Client.Patch(ctx, node, client.MergeFrom(base)); err != nil {
				return fmt.Errorf("uncordon node: %w", err)
			}
		}
		action.Status.ExecutionState = "Uncordoned"
		return InProgress("node uncordoned")
	case "Uncordoned":
		return nil
	default:
		return fmt.Errorf("unknown node maintenance checkpoint %q", action.Status.ExecutionState)
	}
}

func (e *NodeMaintenanceExecutor) Verify(ctx context.Context, action *api.Action) (bool, error) {
	node := &corev1.Node{}
	if err := e.Client.Get(ctx, types.NamespacedName{Name: action.Spec.Target.Name}, node); err != nil {
		return false, err
	}
	return nodeReady(node) && !node.Spec.Unschedulable, nil
}

func (e *NodeMaintenanceExecutor) evict(ctx context.Context, nodeName string) (bool, error) {
	pods := &corev1.PodList{}
	if err := e.Client.List(ctx, pods, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		return false, fmt.Errorf("list node pods: %w", err)
	}
	remaining := false
	for i := range pods.Items {
		pod := &pods.Items[i]
		if skipDrainPod(pod) {
			continue
		}
		if !hasController(pod.OwnerReferences) {
			return false, fmt.Errorf("basic eviction precheck: pod %s/%s is unmanaged", pod.Namespace, pod.Name)
		}
		remaining = true
		eviction := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace}}
		err := e.Kube.PolicyV1().Evictions(pod.Namespace).Evict(ctx, eviction)
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsTooManyRequests(err) {
			return true, fmt.Errorf("evict pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
	}
	return remaining, nil
}

func nodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func skipDrainPod(pod *corev1.Pod) bool {
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed || pod.DeletionTimestamp != nil {
		return true
	}
	if _, mirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
		return true
	}
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

func hasController(refs []metav1.OwnerReference) bool {
	for _, owner := range refs {
		if owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}
