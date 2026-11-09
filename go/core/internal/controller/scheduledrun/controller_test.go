package scheduledrun

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
)

type controllerTestStore struct {
	controllerStore
	session *apiv1.Session
	task    *a2atype.Task
	read    func()
}

func (s controllerTestStore) GetSession(context.Context, string, string) (*apiv1.Session, error) {
	if s.session == nil {
		return nil, database.ErrNotFound
	}
	return s.session, nil
}

func (s controllerTestStore) GetSessionTask(context.Context, string, string, *int) (*a2atype.Task, error) {
	if s.read != nil {
		s.read()
	}
	if s.task == nil {
		return nil, database.ErrNotFound
	}
	return s.task, nil
}
func (s controllerTestStore) GetSessionTaskByMessage(ctx context.Context, session, _, _ string) (*a2atype.Task, error) {
	return s.GetSessionTask(ctx, session, "", nil)
}

type controllerTestCleanup struct {
	controllerWorkflow
	a2asrv.RequestHandler
	task             *a2atype.Task
	canceledTask     *a2atype.Task
	err              error
	deletes, expires int
}

func (c *controllerTestCleanup) Delete(_ context.Context, session *apiv1.Session, _ bool) (*apiv1.Session, error) {
	c.deletes++
	return session, c.err
}

func (c *controllerTestCleanup) CancelTask(_ context.Context, request *a2atype.CancelTaskRequest) (*a2atype.Task, error) {
	c.expires++
	if c.err != nil {
		return nil, c.err
	}
	if c.canceledTask != nil {
		return c.canceledTask, nil
	}
	if c.task != nil {
		return c.task, nil
	}
	now := time.Now()
	return &a2atype.Task{ID: request.ID, Status: a2atype.TaskStatus{State: a2atype.TaskStateCanceled, Timestamp: &now}}, nil
}

func (c *controllerTestCleanup) GetTask(context.Context, *a2atype.GetTaskRequest) (*a2atype.Task, error) {
	if c.task != nil {
		return c.task, nil
	}
	return nil, a2atype.ErrTaskNotFound
}

func (c *controllerTestCleanup) Suspend(_ context.Context, session *apiv1.Session) (*apiv1.Session, error) {
	c.expires++
	return session, c.err
}

func TestControllerRecoversCleanupWithoutReplacingSession(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state apiv1.RuntimeState
	}{
		{"creating timeout", apiv1.RuntimeState_RUNTIME_STATE_CREATING},
		{"running timeout", apiv1.RuntimeState_RUNTIME_STATE_READY},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deadline := time.Now().Add(-time.Minute)
			execution := &apiv1.ScheduledRunExecution{Id: "execution", SessionId: "original", TaskId: "original-task", State: apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_PENDING, Deadline: timestamppb.New(deadline)}
			store := controllerTestStore{session: &apiv1.Session{Id: "original", State: tc.state}}
			cleanup := &controllerTestCleanup{err: errors.New("Substrate unavailable")}
			controller := NewController(store, cleanup, cleanup, nil, time.Second)
			require.Error(t, controller.reconcile(t.Context(), database.LeasedScheduledRunExecution{Execution: execution}))
			require.Equal(t, apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_PENDING, execution.State)
			cleanup.err = nil
			require.NoError(t, controller.reconcile(t.Context(), database.LeasedScheduledRunExecution{Execution: execution}))
			require.Equal(t, apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_TIMED_OUT, execution.State)
			require.Equal(t, "original", execution.SessionId)
			require.Equal(t, 2, cleanup.deletes+cleanup.expires)
		})
	}
	// No reserve method is supplied: a missing historical session must never
	// call it, even when the execution was still PENDING when deletion happened.
	execution := &apiv1.ScheduledRunExecution{Id: "execution", SessionId: "deleted", Deadline: timestamppb.New(time.Now().Add(time.Minute))}
	require.NoError(t, NewController(controllerTestStore{}, nil, nil, nil, time.Second).reconcile(t.Context(), database.LeasedScheduledRunExecution{Execution: execution}))
	require.Equal(t, apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_FAILED, execution.State)
	require.Equal(t, "deleted", execution.SessionId)
}

func TestControllerKeepsCompletedOutcomeAfterDeadline(t *testing.T) {
	completedAt := time.Now().Add(-2 * time.Minute)
	execution := &apiv1.ScheduledRunExecution{Id: "execution", SessionId: "original", TaskId: "original-task", Deadline: timestamppb.New(completedAt.Add(time.Minute))}
	store := controllerTestStore{session: &apiv1.Session{Id: "original"}}
	gateway := &controllerTestCleanup{task: &a2atype.Task{ID: "original-task", Status: a2atype.TaskStatus{State: a2atype.TaskStateCompleted, Timestamp: &completedAt}}}
	store.task = gateway.task
	require.NoError(t, NewController(store, nil, gateway, nil, time.Second).reconcile(t.Context(), database.LeasedScheduledRunExecution{Execution: execution}))
	require.Equal(t, apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_SUCCEEDED, execution.State)
}

func TestControllerDeadlineCancellationOutcome(t *testing.T) {
	deadline := time.Now().Add(-time.Minute)
	before := deadline.Add(-time.Second)
	after := deadline.Add(time.Second)
	for _, tc := range []struct {
		name      string
		state     a2atype.TaskState
		timestamp *time.Time
		want      apiv1.ScheduledRunExecutionState
	}{
		{"canceled with a slow runtime clock", a2atype.TaskStateCanceled, &before, apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_TIMED_OUT},
		{"canceled at deadline", a2atype.TaskStateCanceled, &deadline, apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_TIMED_OUT},
		{"canceled after deadline", a2atype.TaskStateCanceled, &after, apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_TIMED_OUT},
		{"canceled without timestamp", a2atype.TaskStateCanceled, nil, apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_TIMED_OUT},
		{"completed before cancellation", a2atype.TaskStateCompleted, &before, apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_SUCCEEDED},
		{"completed after deadline", a2atype.TaskStateCompleted, &after, apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_TIMED_OUT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execution := &apiv1.ScheduledRunExecution{Id: "execution", SessionId: "original", TaskId: "original-task", Deadline: timestamppb.New(deadline)}
			store := controllerTestStore{session: &apiv1.Session{Id: "original"}}
			gateway := &controllerTestCleanup{
				task:         &a2atype.Task{ID: "original-task", Status: a2atype.TaskStatus{State: a2atype.TaskStateWorking}},
				canceledTask: &a2atype.Task{ID: "original-task", Status: a2atype.TaskStatus{State: tc.state, Timestamp: tc.timestamp}},
			}
			require.NoError(t, NewController(store, nil, gateway, nil, time.Second).reconcile(t.Context(), database.LeasedScheduledRunExecution{Execution: execution}))
			require.Equal(t, 1, gateway.expires)
			require.Equal(t, tc.want, execution.State)
		})
	}
}

func TestExecutionTaskUsesDurableIdentity(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, test := range []struct {
			name string
			task *a2atype.Task
			err  error
		}{
			{name: "found", task: &a2atype.Task{ID: "stored-task"}},
			{name: "missing", err: database.ErrNotFound},
			{name: "store unavailable", err: errors.New("store unavailable")},
		} {
			t.Run(test.name+fmt.Sprint(linked), func(t *testing.T) {
				store := &taskLookupStore{t: t, linked: linked, task: test.task, err: test.err}
				execution := &apiv1.ScheduledRunExecution{Id: "execution", SessionId: "session"}
				if linked {
					execution.TaskId = "stored-task"
				}
				got, err := NewController(store, nil, nil, nil, time.Second).executionTask(t.Context(), execution)
				if errors.Is(test.err, database.ErrNotFound) {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, test.err)
				}
				require.Same(t, test.task, got)
				require.Equal(t, 1, store.calls)
			})
		}
	}
}

type taskLookupStore struct {
	controllerStore
	t      *testing.T
	linked bool
	task   *a2atype.Task
	err    error
	calls  int
}

func (s *taskLookupStore) GetSessionTask(_ context.Context, session, task string, history *int) (*a2atype.Task, error) {
	s.calls++
	require.True(s.t, s.linked)
	require.Equal(s.t, "session", session)
	require.Equal(s.t, "stored-task", task)
	require.NotNil(s.t, history)
	require.Zero(s.t, *history)
	return s.task, s.err
}
func (s *taskLookupStore) GetSessionTaskByMessage(_ context.Context, session, task, message string) (*a2atype.Task, error) {
	s.calls++
	require.False(s.t, s.linked)
	require.Equal(s.t, "session", session)
	require.Empty(s.t, task)
	require.Equal(s.t, "scheduled-run/execution", message)
	return s.task, s.err
}

func TestControllerDoesNotRepeatAnUncertainDispatch(t *testing.T) {
	execution := &apiv1.ScheduledRunExecution{Id: "execution", SessionId: "session", State: apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_RUNNING, Deadline: timestamppb.New(time.Now().Add(time.Minute))}
	reads := 0
	store := controllerTestStore{session: &apiv1.Session{Id: "session", State: apiv1.RuntimeState_RUNTIME_STATE_READY, Operation: apiv1.RuntimeOperation_RUNTIME_OPERATION_NONE}, read: func() { reads++ }}
	controller := NewController(store, nil, nil, nil, time.Second)
	require.NoError(t, controller.reconcile(t.Context(), database.LeasedScheduledRunExecution{Execution: execution}))
	require.Equal(t, 1, reads)
	require.Empty(t, execution.TaskId)
	require.Equal(t, apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_RUNNING, execution.State)
}

type deniedExecutionAuthority struct{ err error }

func (a deniedExecutionAuthority) ProvisioningContext(context.Context, string) (context.Context, error) {
	return nil, a.err
}

func TestControllerAuthorizesBeforeProvisioningOrClaimingDispatch(t *testing.T) {
	denial := errors.New("initiator dependency revoked")
	execution := &apiv1.ScheduledRunExecution{Id: "execution", SessionId: "session", State: apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_PENDING, Deadline: timestamppb.New(time.Now().Add(time.Minute))}
	store := controllerTestStore{session: &apiv1.Session{Id: "session", State: apiv1.RuntimeState_RUNTIME_STATE_CREATING}}
	controller := NewController(store, nil, nil, deniedExecutionAuthority{err: denial}, time.Second)
	require.ErrorIs(t, controller.reconcile(t.Context(), database.LeasedScheduledRunExecution{Execution: execution}), denial)
	require.Equal(t, apiv1.ScheduledRunExecutionState_SCHEDULED_RUN_EXECUTION_STATE_PENDING, execution.State)
}
