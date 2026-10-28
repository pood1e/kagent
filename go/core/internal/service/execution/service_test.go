package execution

import (
	"context"
	"encoding/base64"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

type executionFixture struct{ executions []*apiv1.TaskExecution }

func (f *executionFixture) GetTaskExecution(_ context.Context, _ string) (*apiv1.TaskExecution, error) {
	return f.executions[0], nil
}
func (f *executionFixture) ListTaskExecutions(_ context.Context, _ string, after string, limit int) ([]*apiv1.TaskExecution, error) {
	items := slices.DeleteFunc(slices.Clone(f.executions), func(item *apiv1.TaskExecution) bool { return item.Task.TaskId <= after })
	return items[:min(len(items), limit)], nil
}
func (*executionFixture) Get(_ context.Context, id string) (*apiv1.Session, error) {
	if id == "denied" {
		return nil, serviceerrors.NewPermissionDenied("Denied", nil)
	}
	return &apiv1.Session{Id: id}, nil
}

func TestExecutionPaginationFiltersBeforeSlicing(t *testing.T) {
	root := &apiv1.TaskReference{TaskId: "a", SessionId: "root"}
	fixture := &executionFixture{executions: []*apiv1.TaskExecution{
		{Task: root, Root: root},
		{Task: &apiv1.TaskReference{TaskId: "b", SessionId: "denied"}, Root: root},
		{Task: &apiv1.TaskReference{TaskId: "c", SessionId: "allowed"}, Root: root},
	}}
	service := NewService(fixture, fixture, nil)
	page, err := service.List(t.Context(), &apiv1.ListTaskExecutionsRequest{RootTaskId: "a", Page: &apiv1.PageRequest{Limit: 1}})
	require.NoError(t, err)
	require.Len(t, page.Executions, 1)
	require.Equal(t, "a", page.Executions[0].Task.TaskId)
	next, err := service.List(t.Context(), &apiv1.ListTaskExecutionsRequest{RootTaskId: "a", Page: &apiv1.PageRequest{Limit: 1, PageToken: page.Page.NextPageToken}})
	require.NoError(t, err)
	require.Len(t, next.Executions, 1)
	require.Equal(t, "c", next.Executions[0].Task.TaskId)
	require.Empty(t, next.Page.NextPageToken)
	for _, request := range []*apiv1.ListTaskExecutionsRequest{
		nil,
		{RootTaskId: "a", Page: &apiv1.PageRequest{Limit: -1}},
		{RootTaskId: "a", Page: &apiv1.PageRequest{PageToken: "%"}},
		{RootTaskId: "a", Page: &apiv1.PageRequest{PageToken: base64.RawURLEncoding.EncodeToString([]byte{0xff})}},
	} {
		_, err := service.List(t.Context(), request)
		require.Equal(t, serviceerrors.CodeInvalidArgument, serviceerrors.CodeOf(err))
	}
}

func (*executionFixture) RevokeExecutionAuthority(context.Context, string) error { return nil }

func (*executionFixture) GetRuntimeRevision(context.Context, string) (*database.RuntimeRevision, error) {
	return &database.RuntimeRevision{}, nil
}
