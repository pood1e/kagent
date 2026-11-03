// Package workflow exposes native declarative authoring and pinned definitions.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kagent-dev/kagent/go/adk/pkg/declarative"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type agentAccess interface {
	Authorize(context.Context, auth.Verb, types.NamespacedName) (types.UID, error)
}
type admission interface {
	Workflow(context.Context, *v1alpha3.Agent, *v1alpha3.AgentTemplate, []byte) (string, error)
	WorkflowRevision(context.Context, types.NamespacedName) (string, error)
}
type sessions interface {
	Get(context.Context, string) (*apiv1.Session, error)
}
type revisions interface {
	GetRuntimeRevision(context.Context, string) (*database.RuntimeRevision, error)
}

type Service struct {
	agents    agentAccess
	admission admission
	sessions  sessions
	revisions revisions
}

func NewService(agents agentAccess, admission admission, sessions sessions, revisions revisions) *Service {
	return &Service{agents: agents, admission: admission, sessions: sessions, revisions: revisions}
}

func Schema() (*structpb.Struct, error) {
	schema, err := declarative.DocumentSchema()
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to derive workflow schema", err)
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to encode workflow schema", err)
	}
	var result structpb.Struct
	if err := result.UnmarshalJSON(data); err != nil {
		return nil, serviceerrors.NewInternal("Failed to encode workflow schema", err)
	}
	return &result, nil
}

func (s *Service) Validate(ctx context.Context, agent *v1alpha3.Agent, template *v1alpha3.AgentTemplate, document []byte) (string, error) {
	if _, err := s.agents.Authorize(ctx, auth.VerbCreate, types.NamespacedName{Namespace: agent.Namespace, Name: agent.Name}); err != nil {
		return "", err
	}
	return s.admission.Workflow(ctx, agent, template, document)
}

func (s *Service) Revision(ctx context.Context, ref types.NamespacedName) (string, error) {
	if _, err := s.agents.Authorize(ctx, auth.VerbGet, ref); err != nil {
		return "", err
	}
	return s.admission.WorkflowRevision(ctx, ref)
}

func (s *Service) Session(ctx context.Context, id string) (*apiv1.GetSessionWorkflowResponse, error) {
	session, err := s.sessions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	revision, err := s.revisions.GetRuntimeRevision(ctx, session.PreparedRevision)
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to load Session revision", err)
	}
	var source struct {
		Workflow *configv1.ADKWorkflowConfig `json:"workflow"`
	}
	if err := json.Unmarshal(revision.SourceSnapshot, &source); err != nil {
		return nil, serviceerrors.NewInternal("Failed to decode Session revision", err)
	}
	document := source.Workflow
	if document == nil {
		return nil, serviceerrors.NewNotFound("Session does not run a declarative workflow", fmt.Errorf("revision %s", session.PreparedRevision))
	}
	return &apiv1.GetSessionWorkflowResponse{Document: document.Document, Sha256: document.Sha256, PreparedRevision: session.PreparedRevision}, nil
}
