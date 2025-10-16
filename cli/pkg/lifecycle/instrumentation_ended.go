package lifecycle

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type InstrumentationEnded struct {
	BaseTransition
}

func (i *InstrumentationEnded) From() State {
	return InstrumentationInProgress
}

func (i *InstrumentationEnded) To() State {
	return InstrumentedState
}

func (i *InstrumentationEnded) Execute(ctx context.Context, obj client.Object, isRemote bool) error {
	return nil
}
