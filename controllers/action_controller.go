package controllers

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
	actionexec "github.com/example/k8s-ops-platform/internal/action"
	"github.com/example/k8s-ops-platform/internal/audit"
	metricsinternal "github.com/example/k8s-ops-platform/internal/metrics"
)

const defaultTimeout = 300 * time.Second

type ActionReconciler struct {
	client.Client
	Executors  map[api.ActionType]actionexec.Executor
	Logger     *slog.Logger
	Now        func() time.Time
	MaxRetries int32
	Audit      audit.Recorder
}

func (r *ActionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&api.Action{}).Complete(r)
}

func (r *ActionReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	action := &api.Action{}
	if err := r.Get(ctx, request.NamespacedName, action); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if Terminal(action.Status.Phase) {
		return ctrl.Result{}, nil
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.MaxRetries == 0 {
		r.MaxRetries = 3
	}
	if r.Logger == nil {
		r.Logger = slog.Default()
	}

	executor, ok := r.Executors[action.Spec.ActionType]
	if !ok {
		return r.fail(ctx, action, fmt.Errorf("unsupported action type %q", action.Spec.ActionType))
	}
	switch action.Status.Phase {
	case "":
		action.Status.Phase = api.ActionPhasePending
		action.Status.Message = "action accepted"
		action.Status.ObservedGeneration = action.Generation
		return r.updateStatus(ctx, action, true)
	case api.ActionPhasePending:
		now := metav1.NewTime(r.Now())
		action.Status.Phase = api.ActionPhaseRunning
		action.Status.StartedAt = &now
		action.Status.Message = "execution started"
		action.Status.ObservedGeneration = action.Generation
		apimeta.SetStatusCondition(&action.Status.Conditions, metav1.Condition{Type: "Executing", Status: metav1.ConditionTrue, Reason: "Started", Message: "action execution started", ObservedGeneration: action.Generation})
		r.audit(action, "started", nil)
		return r.updateStatus(ctx, action, true)
	case api.ActionPhaseRunning:
		if r.timedOut(action) {
			return r.fail(ctx, action, fmt.Errorf("action timed out during execution"))
		}
		before := action.Status.DeepCopyValue()
		if preparer, ok := executor.(actionexec.Preparer); ok {
			if err := preparer.Prepare(ctx, action); err != nil {
				return r.retryOrFail(ctx, action, err)
			}
			if !reflect.DeepEqual(before, action.Status) {
				action.Status.Message = "execution checkpoint persisted"
				return r.updateStatus(ctx, action, true)
			}
		}
		before = action.Status.DeepCopyValue()
		err := executor.Execute(ctx, action)
		if !reflect.DeepEqual(before, action.Status) {
			if err != nil {
				action.Status.Message = err.Error()
			}
			return r.updateStatus(ctx, action, true)
		}
		if actionexec.IsInProgress(err) {
			action.Status.Message = err.Error()
			return r.updateStatus(ctx, action, true)
		}
		if err != nil {
			return r.retryOrFail(ctx, action, err)
		}
		action.Status.Phase = api.ActionPhaseVerifying
		action.Status.Message = "execution complete; verifying recovery"
		apimeta.SetStatusCondition(&action.Status.Conditions, metav1.Condition{Type: "Executing", Status: metav1.ConditionFalse, Reason: "Completed", Message: "side effect completed", ObservedGeneration: action.Generation})
		apimeta.SetStatusCondition(&action.Status.Conditions, metav1.Condition{Type: "Verified", Status: metav1.ConditionFalse, Reason: "InProgress", Message: "recovery verification in progress", ObservedGeneration: action.Generation})
		return r.updateStatus(ctx, action, true)
	case api.ActionPhaseVerifying:
		if r.timedOut(action) {
			return r.fail(ctx, action, fmt.Errorf("action timed out during verification"))
		}
		verified, err := executor.Verify(ctx, action)
		if err != nil {
			return r.retryOrFail(ctx, action, err)
		}
		if !verified {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		now := metav1.NewTime(r.Now())
		action.Status.Phase = api.ActionPhaseSucceeded
		action.Status.CompletedAt = &now
		action.Status.Message = "recovery verified"
		apimeta.SetStatusCondition(&action.Status.Conditions, metav1.Condition{Type: "Verified", Status: metav1.ConditionTrue, Reason: "Recovered", Message: "target recovery verified", ObservedGeneration: action.Generation})
		r.audit(action, "succeeded", nil)
		result, updateErr := r.updateStatus(ctx, action, false)
		if updateErr == nil {
			metricsinternal.ObserveAction(action, "succeeded", metricsinternal.Duration(action, r.Now()))
		}
		return result, updateErr
	default:
		return r.fail(ctx, action, fmt.Errorf("invalid action phase %q", action.Status.Phase))
	}
}

func (r *ActionReconciler) retryOrFail(ctx context.Context, action *api.Action, cause error) (ctrl.Result, error) {
	action.Status.RetryCount++
	action.Status.Message = cause.Error()
	apimeta.SetStatusCondition(&action.Status.Conditions, metav1.Condition{Type: "Degraded", Status: metav1.ConditionTrue, Reason: "OperationError", Message: cause.Error(), ObservedGeneration: action.Generation})
	if action.Status.RetryCount >= r.MaxRetries {
		return r.fail(ctx, action, cause)
	}
	result, err := r.updateStatus(ctx, action, false)
	if err == nil {
		result.RequeueAfter = wait.Jitter(2*time.Second, 0.25)
	}
	return result, err
}

func (r *ActionReconciler) fail(ctx context.Context, action *api.Action, cause error) (ctrl.Result, error) {
	now := metav1.NewTime(r.Now())
	action.Status.Phase = api.ActionPhaseFailed
	action.Status.CompletedAt = &now
	action.Status.Message = cause.Error()
	apimeta.SetStatusCondition(&action.Status.Conditions, metav1.Condition{Type: "Verified", Status: metav1.ConditionFalse, Reason: "Failed", Message: cause.Error(), ObservedGeneration: action.Generation})
	r.audit(action, "failed", cause)
	result, updateErr := r.updateStatus(ctx, action, false)
	if updateErr == nil {
		metricsinternal.ObserveAction(action, "failed", metricsinternal.Duration(action, r.Now()))
	}
	return result, updateErr
}

func (r *ActionReconciler) timedOut(action *api.Action) bool {
	if action.Status.StartedAt == nil {
		return false
	}
	timeout := defaultTimeout
	if action.Spec.TimeoutSeconds > 0 {
		timeout = time.Duration(action.Spec.TimeoutSeconds) * time.Second
	}
	return r.Now().After(action.Status.StartedAt.Add(timeout))
}

func (r *ActionReconciler) updateStatus(ctx context.Context, action *api.Action, requeue bool) (ctrl.Result, error) {
	err := r.Status().Update(ctx, action)
	if apierrors.IsConflict(err) {
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{Requeue: requeue}, err
}

func (r *ActionReconciler) audit(action *api.Action, result string, err error) {
	if r.Audit == nil {
		r.Audit = audit.LogRecorder{Logger: r.Logger}
	}
	r.Audit.Record(action, result, err)
}

var _ = types.NamespacedName{}
