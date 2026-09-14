package action

import (
	"context"
	"errors"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
)

type Executor interface {
	Execute(context.Context, *api.Action) error
	Verify(context.Context, *api.Action) (bool, error)
}

type Preparer interface {
	Prepare(context.Context, *api.Action) error
}

type InProgressError struct{ Message string }

func (e *InProgressError) Error() string { return e.Message }

func InProgress(message string) error { return &InProgressError{Message: message} }

func IsInProgress(err error) bool {
	var target *InProgressError
	return errors.As(err, &target)
}
