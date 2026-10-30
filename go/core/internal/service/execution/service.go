// Package execution exposes trusted native task ancestry with Session access
// checks and authorization-aware pagination.
package execution

import (
	"context"
	"encoding/base64"
	"errors"
	"unicode/utf8"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/executionauth"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

type Store interface {
	GetRuntimeRevision(context.Context, string) (*database.RuntimeRevision, error)
	GetTaskExecution(context.Context, string) (*apiv1.TaskExecution, error)
	GetTaskContinuationState(context.Context, string, string) (*apiv1.TaskReference, bool, error)
	ListTaskExecutions(context.Context, string, string, int) ([]*apiv1.TaskExecution, error)
}

type Sessions interface {
	Get(context.Context, string) (*apiv1.Session, error)
	RevokeExecutionAuthority(context.Context, string) error
}

type Service struct {
	store     Store
	sessions  Sessions
	authority *executionauth.Service
}

func NewService(store Store, sessions Sessions, authority *executionauth.Service) *Service {
	return &Service{store: store, sessions: sessions, authority: authority}
}

func (s *Service) Get(ctx context.Context, taskID string) (*apiv1.TaskExecution, error) {
	result, err := s.store.GetTaskExecution(ctx, taskID)
	if errors.Is(err, database.ErrNotFound) {
		return nil, serviceerrors.NewNotFound("Task execution not found", err)
	}
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to read task execution", err)
	}
	if _, err := s.sessions.Get(ctx, result.Task.SessionId); err != nil {
		return nil, err
	}
	return result, nil
}

// Inspect observes native continuation progress after authorizing the anchor.
func (s *Service) Inspect(ctx context.Context, taskID string) (*apiv1.GetTaskExecutionResponse, error) {
	execution, err := s.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	latest, pending, err := s.store.GetTaskContinuationState(ctx, execution.Task.SessionId, taskID)
	if errors.Is(err, database.ErrNotFound) {
		return nil, serviceerrors.NewNotFound("Task execution not found", err)
	}
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to inspect native continuation", err)
	}
	return &apiv1.GetTaskExecutionResponse{Execution: execution, LatestTask: latest, ContinuationPending: pending}, nil
}

func (s *Service) List(ctx context.Context, request *apiv1.ListTaskExecutionsRequest) (*apiv1.ListTaskExecutionsResponse, error) {
	if request == nil || request.RootTaskId == "" || request.GetPage().GetLimit() < 0 || request.GetPage().GetLimit() > 100 {
		return nil, serviceerrors.NewInvalidArgument("Invalid execution page", nil)
	}
	root, err := s.Get(ctx, request.RootTaskId)
	if err != nil {
		return nil, err
	}
	if root.Task.TaskId != root.Root.TaskId {
		return nil, serviceerrors.NewInvalidArgument("root_task_id must identify an execution root", nil)
	}
	afterBytes, err := base64.RawURLEncoding.DecodeString(request.GetPage().GetPageToken())
	if err != nil || len(afterBytes) > 256 || !utf8.Valid(afterBytes) {
		return nil, serviceerrors.NewInvalidArgument("Invalid execution page token", err)
	}
	after := string(afterBytes)
	limit := int(request.GetPage().GetLimit())
	if limit == 0 {
		limit = 50
	}
	result := &apiv1.ListTaskExecutionsResponse{Page: &apiv1.PageResponse{}}
	for len(result.Executions) <= limit {
		items, err := s.store.ListTaskExecutions(ctx, root.Task.TaskId, after, 100)
		if err != nil {
			return nil, serviceerrors.NewInternal("Failed to list task executions", err)
		}
		for _, item := range items {
			after = item.Task.TaskId
			if _, err := s.sessions.Get(ctx, item.Task.SessionId); err != nil {
				if serviceerrors.CodeOf(err) == serviceerrors.CodePermissionDenied || serviceerrors.CodeOf(err) == serviceerrors.CodeNotFound {
					continue
				}
				return nil, err
			}
			result.Executions = append(result.Executions, item)
			if len(result.Executions) > limit {
				result.Executions = result.Executions[:limit]
				result.Page.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(result.Executions[limit-1].Task.TaskId))
				return result, nil
			}
		}
		if len(items) < 100 {
			break
		}
	}
	return result, nil
}
