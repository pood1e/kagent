package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	adka2a "github.com/kagent-dev/kagent/go/adk/pkg/a2a"
	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/a2a"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
	"github.com/kagent-dev/kagent/go/core/internal/service/checkpoint"
	"github.com/kagent-dev/kagent/go/core/internal/service/kubecrud"
	"github.com/kagent-dev/kagent/go/core/internal/service/sandbox"
	sessionsvc "github.com/kagent-dev/kagent/go/core/internal/service/session"
	"github.com/kagent-dev/kagent/go/core/internal/version"
)

const (
	listToolName            = "list_sessions"
	invokeToolName          = "invoke_session"
	defaultTaskPollInterval = 1000
)

type Handler struct {
	sessions      *sessionsvc.Service
	checkpoints   *checkpoint.Service
	gateway       a2asrv.RequestHandler
	http          http.Handler
	continuations *sessionsvc.Continuations
	collaboration *Collaboration
}

type ListSessionsInput = apiv1.ListSessionsInput

type SessionSummary = apiv1.SessionSummary

type ListSessionsOutput = apiv1.ListSessionsOutput

type InvokeSessionInput = apiv1.InvokeSessionInput

type InvokeSessionOutput = apiv1.InvokeSessionOutput

func New(sessions *sessionsvc.Service, checkpoints *checkpoint.Service, gateway a2asrv.RequestHandler, sandboxes *sandbox.Service, templates *kubecrud.Service[*v1alpha3.SandboxTemplate, *v1alpha3.SandboxTemplateList], collaboration *Collaboration) (*Handler, error) {
	if sessions == nil || checkpoints == nil || gateway == nil {
		return nil, fmt.Errorf("session service, checkpoint service, and A2A gateway are required")
	}
	h := &Handler{sessions: sessions, checkpoints: checkpoints, gateway: gateway, collaboration: collaboration}
	capabilities := &mcp.ServerCapabilities{}
	capabilities.AddExtension(tasksExtension, nil)
	instructions := "Use Sessions for agent conversations through A2A. Use checkpoint tools to save and fork Session state."
	if sandboxes != nil {
		instructions += "\n\n" + sandboxInstructions
	}
	server := mcp.NewServer(
		&mcp.Implementation{Name: "kagent", Version: version.Version},
		&mcp.ServerOptions{Capabilities: capabilities, Instructions: instructions},
	)
	if err := addProtoTool(server, &mcp.Tool{
		Name:        listToolName,
		Description: "List ready Sessions visible to the caller",
	}, func() *ListSessionsInput { return &ListSessionsInput{} }, func() *ListSessionsOutput { return &ListSessionsOutput{} }, h.listSessions); err != nil {
		return nil, err
	}
	if err := addProtoTool(server, &mcp.Tool{
		Name:        invokeToolName,
		Description: "Invoke a Session through the public A2A gateway",
	}, func() *InvokeSessionInput { return &InvokeSessionInput{} }, func() *InvokeSessionOutput { return &InvokeSessionOutput{} }, h.invokeSession); err != nil {
		return nil, err
	}
	h.registerCheckpointTools(server)
	if err := h.registerCollaborationTools(server); err != nil {
		return nil, err
	}
	registerSandboxTools(server, sandboxes, templates)
	if sandboxes != nil {
		registerSandboxPrompts(server)
	}
	server.AddReceivingMiddleware(h.taskAwareToolCall)
	if err := h.registerTaskMethods(server); err != nil {
		return nil, err
	}
	h.http = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tag, err := apia2a.ParseAcceptLanguage(r.Header.Get("Accept-Language"))
	if err != nil {
		http.Error(w, "invalid Accept-Language", http.StatusBadRequest)
		return
	}
	h.http.ServeHTTP(w, r.WithContext(apia2a.WithResponseLocale(r.Context(), tag)))
}

// WithContinuations enables durable native synchronous delegation.
func (h *Handler) WithContinuations(continuations *sessionsvc.Continuations) {
	h.continuations = continuations
}

func (h *Handler) listSessions(ctx context.Context, _ *mcp.CallToolRequest, input *ListSessionsInput) (*mcp.CallToolResult, *ListSessionsOutput, error) {
	result, err := h.sessions.List(ctx, sessionsvc.ListRequest{
		PageSize: int(input.PageSize), PageToken: input.PageToken,
	})
	if err != nil {
		return toolError(err), &ListSessionsOutput{}, nil
	}
	output := &ListSessionsOutput{Sessions: []*SessionSummary{}, NextPageToken: result.NextPageToken}
	for _, session := range result.Sessions {
		if session.GetState() != apiv1.RuntimeState_RUNTIME_STATE_READY {
			continue
		}
		output.Sessions = append(output.Sessions, sessionSummary(session))
	}
	var text strings.Builder
	for i, session := range output.Sessions {
		if i > 0 {
			text.WriteByte('\n')
		}
		fmt.Fprintf(&text, "%s (%s)", session.Id, session.Agent)
	}
	if text.Len() == 0 {
		text.WriteString("No ready Sessions found.")
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text.String()}}}, output, nil
}

func (h *Handler) invokeSession(ctx context.Context, _ *mcp.CallToolRequest, input *InvokeSessionInput) (*mcp.CallToolResult, *InvokeSessionOutput, error) {
	task, err := h.invoke(ctx, input, input.Background)
	if err != nil {
		return toolError(err), &InvokeSessionOutput{}, nil
	}
	result, output := invocationResult(input, task)
	if input.Background {
		result.Content = textContent("%s", output.TaskId)
	}
	return result, output, nil
}

func (h *Handler) invoke(ctx context.Context, input *InvokeSessionInput, async bool) (*a2atype.Task, error) {
	if input.GetSessionId() == "" || strings.TrimSpace(input.GetMessage()) == "" {
		return nil, fmt.Errorf("session_id and message are required")
	}
	message := a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart(input.GetMessage()))
	if input.GetMessageId() != "" {
		message.ID = input.GetMessageId()
	}
	session, err := h.sessions.Get(ctx, input.GetSessionId())
	if err != nil {
		return nil, err
	}
	message.ContextID = session.GetId()
	apia2a.SetResponseLocale(message, apia2a.ContextResponseLocale(ctx))
	if origin := runtimeauth.Origin(ctx); !async && origin != nil && h.continuations != nil {
		if err := h.continuations.Register(ctx, origin.SessionID, session.Id, message.ID); err != nil {
			return nil, err
		}
	}
	// Direct gateway calls carry the Agent tenant on the request, like A2A transports do.
	tenant := session.GetAgent().GetNamespace() + "/" + session.GetAgent().GetName()
	routed := interactionContext(ctx)
	var taskID a2atype.TaskID
	for event, err := range h.gateway.SendStreamingMessage(routed, &a2atype.SendMessageRequest{Tenant: tenant, Message: message, Config: &a2atype.SendMessageConfig{ReturnImmediately: input.Background}}) {
		if err != nil {
			return nil, err
		}
		if event != nil && event.TaskInfo().TaskID != "" {
			taskID = event.TaskInfo().TaskID
			if async {
				break
			}
		}
	}
	if taskID == "" {
		return nil, fmt.Errorf("A2A runtime did not return a task")
	}
	// The runtime continues after this observer leaves. Public responses use
	// the persisted task, including the lifecycle publication boundary.
	return h.gateway.GetTask(routed, &a2atype.GetTaskRequest{Tenant: tenant, ID: taskID})
}

func interactionContext(ctx context.Context) context.Context {
	ctx, _ = a2asrv.NewCallContext(ctx, a2asrv.NewServiceParams(map[string][]string{
		a2atype.SvcParamExtensions: {adka2a.HITLExtensionURI},
	}))
	return ctx
}

func invocationResult(input *InvokeSessionInput, task *a2atype.Task) (*mcp.CallToolResult, *InvokeSessionOutput) {
	text := a2a.TaskText(task)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		IsError: task.Status.State == a2atype.TaskStateFailed ||
			task.Status.State == a2atype.TaskStateRejected ||
			task.Status.State == a2atype.TaskStateAuthRequired,
	}, &InvokeSessionOutput{
		SessionId: input.GetSessionId(),
		TaskId:    string(task.ID), ContextId: task.ContextID,
		State: a2apb.TaskState(a2apb.TaskState_value[string(task.Status.State)]), Text: text,
	}
}

func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}, IsError: true}
}
