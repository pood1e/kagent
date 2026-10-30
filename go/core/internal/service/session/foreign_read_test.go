package session

import (
	"context"
	"errors"
	"testing"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type foreignSessionStore struct {
	*serviceTestStore
	session *apiv1.Session
	reads   int
}

func (s *foreignSessionStore) GetSession(context.Context, string, string) (*apiv1.Session, error) {
	return nil, database.ErrNotFound
}

func (s *foreignSessionStore) GetSessionByID(context.Context, string) (*apiv1.Session, error) {
	s.reads++
	if s.session == nil {
		return nil, database.ErrNotFound
	}
	return s.session, nil
}

type allCreatorsAuthorizer struct{ allow bool }

func (a allCreatorsAuthorizer) Check(_ context.Context, _ auth.Principal, verb auth.Verb, resource auth.Resource) error {
	if resource.Type == "SessionAllCreators" && (verb != auth.VerbGet || !a.allow) {
		return errors.New("all-creators read denied")
	}
	return nil
}

func TestServiceGetForeignSessionRequiresAllCreatorsRead(t *testing.T) {
	id := uuid.NewString()
	foreign := &apiv1.Session{Id: id, Creator: "bob"}
	for _, test := range []struct {
		name  string
		ctx   context.Context
		allow bool
		found bool
	}{
		{name: "administrator", ctx: serviceTestContext("alice"), allow: true, found: true},
		{name: "ordinary caller", ctx: serviceTestContext("alice")},
		{name: "empty principal", ctx: serviceTestContext(""), allow: true},
		{name: "share", ctx: auth.ShareContextTo(serviceTestContext("alice"), &auth.ShareContext{SessionID: id, UserID: "bob"}), allow: true},
		{name: "execution credential", ctx: auth.AuthSessionTo(t.Context(), auth.NewCredentialSession(auth.Principal{ID: "alice", Execution: &apiv1.SessionAuthority{SessionId: uuid.NewString()}}, nil)), allow: true},
		{name: "runtime actor", ctx: auth.AuthSessionTo(t.Context(), auth.NewCredentialSession(auth.Principal{ID: "alice", RuntimeBinding: &auth.RuntimeBinding{SessionID: uuid.NewString()}}, nil)), allow: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &foreignSessionStore{serviceTestStore: &serviceTestStore{}, session: foreign}
			service := NewService(store, allCreatorsAuthorizer{allow: test.allow}, nil)
			result, err := service.Get(test.ctx, id)
			if test.found {
				if err != nil || result != foreign || store.reads != 1 {
					t.Fatalf("Get() = %v, %v; unscoped reads = %d, want foreign Session", result, err, store.reads)
				}
			} else if !serviceerrors.IsCode(err, serviceerrors.CodeNotFound) || store.reads != 0 {
				t.Fatalf("Get() = %v, %v; unscoped reads = %d, want NotFound without unscoped read", result, err, store.reads)
			}
			reads := store.reads
			code := serviceerrors.CodeNotFound
			if test.name == "share" {
				code = serviceerrors.CodePermissionDenied
			}
			if _, err := service.getAuthorized(test.ctx, id, auth.VerbUpdate); !serviceerrors.IsCode(err, code) || store.reads != reads {
				t.Fatalf("update lookup = %v; unscoped reads = %d, want %s without unscoped read", err, store.reads, code)
			}
		})
	}
}

func TestInteractionsListForeignTasksWithAllCreatorsRead(t *testing.T) {
	id := uuid.NewString()
	agent := types.NamespacedName{Namespace: "team-a", Name: "assistant"}
	foreign := &apiv1.Session{Id: id, Creator: "bob", Agent: &apiv1.ResourceReference{Namespace: agent.Namespace, Name: agent.Name}}
	for _, allowed := range []bool{false, true} {
		store := &foreignSessionStore{serviceTestStore: &serviceTestStore{}, session: foreign}
		tasks := &interactionTestStore{task: &a2atype.Task{ID: "task", ContextID: id}}
		service := NewInteractionService(tasks, nil, NewService(store, allCreatorsAuthorizer{allow: allowed}, nil), false)
		result, err := service.ListTasks(serviceTestContext("alice"), agent, &a2atype.ListTasksRequest{ContextID: id})
		if allowed {
			if err != nil || len(result.Tasks) != 1 || result.Tasks[0].ID != "task" || tasks.taskReads != 1 {
				t.Fatalf("ListTasks(allCreators=%t) = %+v, %v; task reads = %d", allowed, result, err, tasks.taskReads)
			}
		} else if !errors.Is(err, a2atype.ErrTaskNotFound) || tasks.taskReads != 0 {
			t.Fatalf("ListTasks(allCreators=%t) = %+v, %v; task reads = %d, want NotFound without task read", allowed, result, err, tasks.taskReads)
		}
	}
}
