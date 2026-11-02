package grpcserver

import (
	"context"

	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/structuredobject"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/internal/service/workflow"
)

type workflowServer struct {
	apiv1.UnimplementedWorkflowServiceServer
	service         *workflow.Service
	maxMessageBytes int
}

func (s *workflowServer) GetWorkflowSchema(context.Context, *apiv1.GetWorkflowSchemaRequest) (*apiv1.GetWorkflowSchemaResponse, error) {
	schema, err := workflow.Schema()
	if err != nil {
		return nil, err
	}
	return &apiv1.GetWorkflowSchemaResponse{Schema: schema}, nil
}

func (s *workflowServer) GetWorkflowRevision(ctx context.Context, request *apiv1.GetWorkflowRevisionRequest) (*apiv1.GetWorkflowRevisionResponse, error) {
	revision, err := s.service.Revision(ctx, types.NamespacedName{Namespace: request.Ref.Namespace, Name: request.Ref.Name})
	if err != nil {
		return nil, err
	}
	return &apiv1.GetWorkflowRevisionResponse{PreparedRevision: revision}, nil
}

func (s *workflowServer) ValidateWorkflow(ctx context.Context, request *apiv1.ValidateWorkflowRequest) (*apiv1.ValidateWorkflowResponse, error) {
	var agent v1alpha3.Agent
	if err := structuredobject.ToGo(request.Resource, agentKind, &agent, s.maxMessageBytes); err != nil {
		return nil, serviceerrors.NewInvalidArgument("Invalid Agent resource", err)
	}
	if agent.Name != "" && agent.Name != request.Ref.Name || agent.Namespace != "" && agent.Namespace != request.Ref.Namespace {
		return nil, serviceerrors.NewInvalidArgument("Agent reference does not match resource metadata", nil)
	}
	agent.Name, agent.Namespace = request.Ref.Name, request.Ref.Namespace
	var template *v1alpha3.AgentTemplate
	if request.AgentTemplate != nil {
		template = &v1alpha3.AgentTemplate{}
		if err := structuredobject.ToGo(request.AgentTemplate, agentTemplateKind, template, s.maxMessageBytes); err != nil {
			return nil, serviceerrors.NewInvalidArgument("Invalid AgentTemplate resource", err)
		}
	}
	revision, err := s.service.Validate(ctx, &agent, template, request.Document)
	if err != nil {
		return nil, err
	}
	return &apiv1.ValidateWorkflowResponse{PreparedRevision: revision}, nil
}

func (s *workflowServer) GetSessionWorkflow(ctx context.Context, request *apiv1.GetSessionWorkflowRequest) (*apiv1.GetSessionWorkflowResponse, error) {
	return s.service.Session(ctx, request.SessionId)
}
