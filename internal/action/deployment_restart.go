package action

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
)

const restartAnnotation = "ops.example.io/restarted-by-action"

type DeploymentRestartExecutor struct{ Client client.Client }

func (e *DeploymentRestartExecutor) Execute(ctx context.Context, action *api.Action) error {
	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: action.Spec.Target.Namespace, Name: action.Spec.Target.Name}
	if err := e.Client.Get(ctx, key, deployment); err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}
	if deployment.Spec.Template.Annotations[restartAnnotation] == string(action.UID) {
		return nil
	}
	base := deployment.DeepCopy()
	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = map[string]string{}
	}
	deployment.Spec.Template.Annotations[restartAnnotation] = string(action.UID)
	deployment.Spec.Template.Annotations["ops.example.io/restarted-at"] = time.Now().UTC().Format(time.RFC3339)
	return e.Client.Patch(ctx, deployment, client.MergeFrom(base))
}

func (e *DeploymentRestartExecutor) Verify(ctx context.Context, action *api.Action) (bool, error) {
	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: action.Spec.Target.Namespace, Name: action.Spec.Target.Name}
	if err := e.Client.Get(ctx, key, deployment); err != nil {
		return false, err
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	return deployment.Spec.Template.Annotations[restartAnnotation] == string(action.UID) &&
		deployment.Status.ObservedGeneration >= deployment.Generation &&
		deployment.Status.UpdatedReplicas >= desired &&
		deployment.Status.AvailableReplicas >= desired, nil
}
