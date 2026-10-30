// Package nativeagent executes declarative nodes through native Session and A2A
// APIs. The control plane owns acceptance, lineage, authority and task state.
package nativeagent

import (
	"context"

	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

type executionKey struct{}

type execution struct {
	task *apiv1.TaskReference
	done <-chan struct{}
}

// WithTask attaches the accepted native task to its runtime invocation.
func WithTask(ctx context.Context, task *apiv1.TaskReference) context.Context {
	return context.WithValue(ctx, executionKey{}, execution{task: proto.CloneOf(task), done: ctx.Done()})
}
