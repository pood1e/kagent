// Package nativeagent executes declarative nodes through native Session and A2A
// APIs. The control plane owns acceptance, lineage, authority and task state.
package nativeagent

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

type executionKey struct{}

type execution struct {
	task     *apiv1.TaskReference
	done     <-chan struct{}
	deadline time.Time
}

// WithTask attaches the accepted native task to its runtime invocation.
func WithTask(ctx context.Context, task *apiv1.TaskReference) context.Context {
	deadline, _ := ctx.Deadline()
	return context.WithValue(ctx, executionKey{}, execution{task: proto.CloneOf(task), done: ctx.Done(), deadline: deadline})
}

func (e execution) stopped() bool {
	select {
	case <-e.done:
		return true
	default:
		return deadlineElapsed(e.deadline)
	}
}

func deadlineElapsed(deadline time.Time) bool {
	return !deadline.IsZero() && !time.Now().Before(deadline)
}
