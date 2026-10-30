package nativeagent_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/adk/v2/session"

	"github.com/kagent-dev/kagent/go/adk/pkg/auth"
	"github.com/kagent-dev/kagent/go/adk/pkg/nativeagent"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

const singleNode = `      - id: call
        type: agent
        agent_ref: {namespace: team, name: analyst}
        output_schema: {type: string}
`

const singleEdge = `      - {from: START, to: call}`

func taskContext(ctx context.Context) context.Context {
	return nativeagent.WithTask(auth.WithUserID(ctx, "reader"), &apiv1.TaskReference{
		SessionId: "4d204c94-5b18-45c9-b719-cd3accf709a3", TaskId: "aaf6ceaa-1e7b-4ccd-973d-9dc87bd9f516",
	})
}

func finalOutput(events []*session.Event) any {
	for _, event := range slices.Backward(events) {
		if event.Output != nil {
			return event.Output
		}
	}
	return nil
}

func TestMixedNativeAgentsUseOneGraphAndIndependentSessions(t *testing.T) {
	server, client := startNativeServer(t, "")
	events, err := runWorkflow(taskContext(t.Context()), client, `
      - {id: standard, type: agent, agent_ref: {namespace: team, name: standard}}
      - {id: codex, type: agent, agent_ref: {namespace: team, name: codex}}
      - {id: claude, type: agent, agent_ref: {namespace: team, name: claude}}
`, `
      - {from: START, to: standard}
      - {from: standard, to: codex}
      - {from: codex, to: claude}
`)
	require.NoError(t, err)
	require.Equal(t, "hello|standard|codex|claude", finalOutput(events))
	server.mu.Lock()
	defer server.mu.Unlock()
	require.Len(t, server.sessions, 3)
	require.Len(t, server.tasks, 3)
	for _, value := range server.sessions {
		require.Equal(t, "revision-"+value.Agent.Name, value.PreparedRevision)
	}
}

func TestRetryAfterAmbiguousAcceptanceReusesNativeTask(t *testing.T) {
	server, client := startNativeServer(t, "drop")
	events, err := runWorkflow(taskContext(t.Context()), client, singleNode+`        retry: {max_attempts: 2, initial_delay: 0s}`+"\n", singleEdge)
	require.NoError(t, err)
	require.Equal(t, "hello|analyst", finalOutput(events))
	server.mu.Lock()
	defer server.mu.Unlock()
	require.Len(t, server.sessions, 1)
	require.Len(t, server.tasks, 1)
	for _, count := range server.sends {
		require.Equal(t, 2, count)
	}
}

func TestNativeApprovalResumesTheAcceptedChild(t *testing.T) {
	server, client := startNativeServer(t, "approval")
	done := make(chan error, 1)
	go func() {
		_, err := runWorkflow(taskContext(t.Context()), client, singleNode, singleEdge)
		done <- err
	}()
	select {
	case <-server.approval:
	case err := <-done:
		t.Fatalf("workflow failed before approval: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("native child did not reach approval")
	}
	select {
	case err := <-done:
		t.Fatalf("orchestration ended before child approval: %v", err)
	default:
	}
	close(server.release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("native approval did not resume observation")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	require.Len(t, server.tasks, 1)
	require.Zero(t, server.canceled)
}

func TestFailedNativeTaskIsNotReexecutedByNodeRetry(t *testing.T) {
	server, client := startNativeServer(t, "failed")
	_, err := runWorkflow(taskContext(t.Context()), client, singleNode+"        retry: {max_attempts: 2, initial_delay: 0s}\n", singleEdge)
	require.ErrorContains(t, err, "TASK_STATE_FAILED")
	server.mu.Lock()
	defer server.mu.Unlock()
	require.Len(t, server.tasks, 1)
	require.Len(t, server.sessions, 1)
	for _, count := range server.sends {
		require.Equal(t, 2, count)
	}
}

func TestNodeTimeoutCancelsItsAcceptedChild(t *testing.T) {
	server, client := startNativeServer(t, "hang")
	_, err := runWorkflow(taskContext(t.Context()), client, singleNode+"        timeout: 100ms\n", singleEdge)
	require.Error(t, err)
	t.Logf("timeout: %v", err)
	server.mu.Lock()
	defer server.mu.Unlock()
	require.Len(t, server.tasks, 1)
	require.Equal(t, 1, server.canceled)
}

func TestRuntimeReplacementKeepsAcceptedChildForReplay(t *testing.T) {
	server, client := startNativeServer(t, "hang")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := runWorkflow(taskContext(ctx), client, singleNode, singleEdge)
		done <- err
	}()
	select {
	case <-server.accepted:
	case err := <-done:
		t.Fatalf("workflow failed before acceptance: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("native task was not accepted")
	}
	cancel()
	require.Error(t, <-done)
	close(server.release)
	events, err := runWorkflow(taskContext(t.Context()), client, singleNode, singleEdge)
	require.NoError(t, err)
	require.Equal(t, "hello|analyst", finalOutput(events))
	server.mu.Lock()
	defer server.mu.Unlock()
	require.Zero(t, server.canceled)
	require.Len(t, server.sessions, 1)
	require.Len(t, server.tasks, 1)
	for _, count := range server.sends {
		require.Equal(t, 2, count)
	}
}
