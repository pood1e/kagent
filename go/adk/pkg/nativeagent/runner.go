package nativeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"buf.build/go/protovalidate"
	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/google/uuid"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/kagent-dev/kagent/go/adk/pkg/controllerclient"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// NewRunner uses the runtime's existing controller connection and caller identity.
func NewRunner(controller *controllerclient.Client) func(agent.Context, *apiv1.ResourceReference, any, func(*session.Event) error) (any, error) {
	if controller == nil {
		return nil
	}
	return func(ctx agent.Context, ref *apiv1.ResourceReference, input any, emit func(*session.Event) error) (result any, err error) {
		defer func() {
			if err != nil {
				err = fmt.Errorf("native Agent %s/%s: %w", ref.GetNamespace(), ref.GetName(), err)
			}
		}()
		origin, ok := ctx.Value(executionKey{}).(execution)
		if !ok || origin.task.GetTaskId() == "" || origin.task.GetSessionId() == "" {
			return nil, fmt.Errorf("native Agent node requires an accepted parent task")
		}
		if err := protovalidate.Validate(ref); err != nil {
			return nil, fmt.Errorf("agent_ref: %w", err)
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("encode native Agent input: %w", err)
		}
		key := invocationKey(origin.task, ctx.Path(), ctx.Branch(), ref, encoded)
		callCtx, cancel := controller.CallContext(ctx, "")
		created, err := controller.SessionService().CreateSession(callCtx, &apiv1.CreateSessionRequest{Agent: ref, RequestId: key})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("create native Agent session: %w", err)
		}
		if created.GetSession().GetId() == "" {
			return nil, fmt.Errorf("native Agent session response has no ID")
		}
		client, err := controller.AgentClient(ctx, ref)
		if err != nil {
			return nil, err
		}
		message := string(encoded)
		if text, ok := input.(string); ok {
			message = text
		}
		request := &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(message))}
		request.Message.ID = key
		request.Message.ContextID = created.GetSession().GetId()
		// Foreground sends let the native gateway bind this node to its working
		// parent. ReturnImmediately would instead create a background execution.
		streamCtx := controller.StreamContext(ctx, "")
		var taskID, anchorID a2a.TaskID
		defer func() {
			if anchorID == "" {
				return
			}
			// The remote deadline can expire before the local timer closes Done.
			deadline, _ := ctx.Deadline()
			if ctx.Err() == nil && !deadlineElapsed(deadline) {
				return
			}
			if origin.stopped() {
				// The native parent boundary owns durable cleanup after context loss.
				return
			}
			cleanup, stop := context.WithTimeout(context.WithoutCancel(streamCtx), 10*time.Second)
			defer stop()
			// A terminal parent also queues durable foreground cleanup; this
			// bounded attempt shortens node-timeout and parallel fail-fast latency.
			if _, cancelErr := client.CancelTask(cleanup, &a2a.CancelTaskRequest{ID: anchorID}); cancelErr != nil {
				err = errors.Join(err, fmt.Errorf("cancel native Agent task: %w", cancelErr))
			}
		}()
		progress := observer{ctx: ctx, ref: ref, emit: emit}
		events := client.SendStreamingMessage(streamCtx, request)
	observe:
		for {
			for event, err := range events {
				if err != nil {
					return nil, fmt.Errorf("observe native Agent: %w", err)
				}
				if event == nil {
					continue
				}
				if info := event.TaskInfo(); info.TaskID != "" {
					taskID = info.TaskID
					if anchorID == "" {
						anchorID = taskID
					}
				}
				if err := progress.observe(taskID, event); err != nil {
					return nil, err
				}
			}
			if anchorID == "" {
				return nil, fmt.Errorf("native Agent stream ended without an accepted task")
			}
			for {
				state, err := getExecution(ctx, controller, anchorID)
				if err != nil {
					return nil, err
				}
				if state.GetLatestTask().GetSessionId() != created.Session.Id || state.GetLatestTask().GetTaskId() == "" {
					return nil, fmt.Errorf("native continuation reference does not belong to the child Session")
				}
				taskID = a2a.TaskID(state.LatestTask.TaskId)
				task, err := getTask(ctx, controller, client, taskID)
				if err != nil {
					return nil, err
				}
				if task.ContextID != created.Session.Id {
					return nil, fmt.Errorf("native task does not belong to the child Session")
				}
				if err := progress.observe(taskID, task); err != nil {
					return nil, err
				}
				switch task.Status.State {
				case a2a.TaskStateCompleted:
					if !state.ContinuationPending {
						return taskOutput(task)
					}
				case a2a.TaskStateFailed, a2a.TaskStateCanceled, a2a.TaskStateRejected:
					return nil, fmt.Errorf("task %s ended %s", task.ID, task.Status.State)
				case a2a.TaskStateInputRequired, a2a.TaskStateAuthRequired:
				default:
					events = client.SubscribeToTask(controller.StreamContext(ctx, ""), &a2a.SubscribeToTaskRequest{ID: taskID})
					continue observe
				}
				// Native dependencies can resume the Agent in a new task. The
				// accepted anchor remains the retry and cancellation identity.
				if err := wait(ctx); err != nil {
					return nil, err
				}
			}
		}
	}
}

func getExecution(ctx context.Context, controller *controllerclient.Client, anchor a2a.TaskID) (*apiv1.GetTaskExecutionResponse, error) {
	ctx, cancel := controller.CallContext(ctx, "")
	defer cancel()
	return controller.ExecutionService().GetTaskExecution(ctx, &apiv1.GetTaskExecutionRequest{TaskId: string(anchor)})
}

func invocationKey(parent *apiv1.TaskReference, path, branch string, ref *apiv1.ResourceReference, input []byte) string {
	data, _ := json.Marshal([]string{parent.SessionId, parent.TaskId, path, branch, ref.Namespace, ref.Name, string(input)})
	return uuid.NewSHA1(uuid.NameSpaceURL, data).String()
}

func getTask(ctx context.Context, controller *controllerclient.Client, client *a2aclient.Client, id a2a.TaskID) (*a2a.Task, error) {
	ctx, cancel := controller.CallContext(ctx, "")
	defer cancel()
	return client.GetTask(ctx, &a2a.GetTaskRequest{ID: id})
}

func wait(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type observer struct {
	ctx   agent.Context
	ref   *apiv1.ResourceReference
	emit  func(*session.Event) error
	state a2a.TaskState
	task  a2a.TaskID
	last  time.Time
}

func (o *observer) observe(id a2a.TaskID, event a2a.Event) error {
	state := o.state
	switch value := event.(type) {
	case *a2a.Task:
		state = value.Status.State
	case *a2a.TaskStatusUpdateEvent:
		state = value.Status.State
	}
	if id == "" || (id == o.task && state == o.state && time.Since(o.last) < 5*time.Second) {
		return nil
	}
	o.state, o.last = state, time.Now()
	o.task = id
	progress := session.NewEvent(o.ctx, o.ctx.InvocationID())
	progress.Content = genai.NewContentFromText(fmt.Sprintf("%s %s · Agent %s/%s · %s · %s", o.ctx.Path(), o.ctx.Branch(), o.ref.Namespace, o.ref.Name, id, state), genai.RoleModel)
	return o.emit(progress)
}
