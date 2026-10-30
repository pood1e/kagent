package nativeagent_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/a2aproject/a2a-go/v2/a2apb/v1/pbconv"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/kagent-dev/kagent/go/adk/pkg/controllerclient"
	"github.com/kagent-dev/kagent/go/adk/pkg/declarative"
	"github.com/kagent-dev/kagent/go/adk/pkg/nativeagent"
	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

type nativeServer struct {
	apiv1.UnimplementedSessionServiceServer
	apiv1.UnimplementedExecutionServiceServer
	a2apb.UnimplementedA2AServiceServer
	mu                       sync.Mutex
	sessions                 map[string]*apiv1.Session
	tasks                    map[string]*a2a.Task
	sends                    map[string]int
	mode                     string
	canceled                 int
	accepted                 chan struct{}
	approval                 chan struct{}
	release                  chan struct{}
	acceptOnce, approvalOnce sync.Once
}

func startNativeServer(t *testing.T, mode string) (*nativeServer, *controllerclient.Client) {
	t.Helper()
	server := &nativeServer{
		sessions: map[string]*apiv1.Session{}, tasks: map[string]*a2a.Task{}, sends: map[string]int{}, mode: mode,
		accepted: make(chan struct{}), approval: make(chan struct{}), release: make(chan struct{}),
	}
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	apiv1.RegisterSessionServiceServer(grpcServer, server)
	apiv1.RegisterExecutionServiceServer(grpcServer, server)
	a2apb.RegisterA2AServiceServer(grpcServer, server)
	served := make(chan error, 1)
	go func() { served <- grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		require.NoError(t, <-served)
	})
	client, err := controllerclient.New(controllerclient.Config{
		APIURL: "http://controller.test", AgentName: "team__NS__workflow",
		DialOptions: []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) })},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return server, client
}

func (s *nativeServer) CreateSession(ctx context.Context, request *apiv1.CreateSessionRequest) (*apiv1.CreateSessionResponse, error) {
	if err := checkIdentity(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.sessions[request.RequestId]
	if value == nil {
		value = &apiv1.Session{Id: uuid.NewString(), Agent: proto.CloneOf(request.Agent), PreparedRevision: "revision-" + request.Agent.Name}
		s.sessions[request.RequestId] = value
	}
	return &apiv1.CreateSessionResponse{Session: proto.CloneOf(value)}, nil
}

func (s *nativeServer) SendStreamingMessage(request *a2apb.SendMessageRequest, stream grpc.ServerStreamingServer[a2apb.StreamResponse]) error {
	if err := checkIdentity(stream.Context()); err != nil {
		return err
	}
	if got := metadata.ValueFromIncomingContext(stream.Context(), strings.ToLower(a2a.SvcParamExtensions)); len(got) != 1 || got[0] != apia2a.HITLExtensionURI {
		return status.Error(codes.InvalidArgument, "native HITL was not negotiated")
	}
	if request.Configuration.GetReturnImmediately() {
		return status.Error(codes.InvalidArgument, "node became background")
	}
	s.mu.Lock()
	key := request.Message.MessageId
	task := s.tasks[key]
	if task == nil {
		agentName := strings.TrimPrefix(request.Tenant, "team/")
		input, err := pbconv.FromProtoMessage(request.Message)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		text := ""
		for _, part := range input.Parts {
			text += part.Text()
		}
		task = &a2a.Task{ID: a2a.TaskID(uuid.NewString()), ContextID: request.Message.ContextId,
			Status:    a2a.TaskStatus{State: a2a.TaskStateWorking},
			Artifacts: []*a2a.Artifact{{ID: "answer", Parts: a2a.ContentParts{a2a.NewTextPart(text + "|" + agentName)}}},
		}
		s.tasks[key] = task
	}
	s.sends[key]++
	attempt := s.sends[key]
	if s.mode == "approval" {
		task.Status.State = a2a.TaskStateInputRequired
	}
	if s.mode == "failed" {
		task.Status.State = a2a.TaskStateFailed
	}
	if s.mode == "continuation" {
		task.Status.State = a2a.TaskStateCompleted
	}
	if s.mode == "" || (s.mode == "drop" && attempt > 1) || (s.mode == "hang" && released(s.release)) {
		task.Status.State = a2a.TaskStateCompleted
	}
	wire, err := pbconv.ToProtoStreamResponse(task)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := stream.Send(wire); err != nil {
		return err
	}
	s.acceptOnce.Do(func() { close(s.accepted) })
	if s.mode == "drop" && attempt == 1 {
		return status.Error(codes.Unavailable, "controller restarted after acceptance")
	}
	if s.mode == "hang" && !released(s.release) {
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	return nil
}

func (s *nativeServer) GetTaskExecution(ctx context.Context, request *apiv1.GetTaskExecutionRequest) (*apiv1.GetTaskExecutionResponse, error) {
	if err := checkIdentity(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, task := range s.tasks {
		if string(task.ID) != request.TaskId {
			continue
		}
		anchor := &apiv1.TaskReference{SessionId: task.ContextID, TaskId: string(task.ID)}
		result := &apiv1.GetTaskExecutionResponse{Execution: &apiv1.TaskExecution{Task: anchor}, LatestTask: proto.CloneOf(anchor)}
		if s.mode == "continuation" {
			s.approvalOnce.Do(func() { close(s.approval) })
			result.ContinuationPending = !released(s.release)
			if !result.ContinuationPending {
				resumed := s.tasks["native-continuation"]
				if resumed == nil {
					resumed = &a2a.Task{ID: a2a.TaskID(uuid.NewString()), ContextID: task.ContextID, Status: a2a.TaskStatus{State: a2a.TaskStateCompleted},
						Artifacts: []*a2a.Artifact{{ID: "final", Parts: a2a.ContentParts{a2a.NewTextPart("final native continuation")}}}}
					s.tasks["native-continuation"] = resumed
				}
				result.LatestTask.TaskId = string(resumed.ID)
			}
		}
		return result, nil
	}
	return nil, status.Error(codes.NotFound, "task not found")
}

func (s *nativeServer) GetTask(ctx context.Context, request *a2apb.GetTaskRequest) (*a2apb.Task, error) {
	if err := checkIdentity(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, task := range s.tasks {
		if string(task.ID) != request.Id {
			continue
		}
		if s.mode == "approval" {
			s.approvalOnce.Do(func() { close(s.approval) })
			if released(s.release) {
				task.Status.State = a2a.TaskStateCompleted
			}
		}
		return pbconv.ToProtoTask(task)
	}
	return nil, status.Error(codes.NotFound, "task not found")
}

func (s *nativeServer) CancelTask(ctx context.Context, request *a2apb.CancelTaskRequest) (*a2apb.Task, error) {
	if err := checkIdentity(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, task := range s.tasks {
		if string(task.ID) == request.Id {
			s.canceled++
			task.Status.State = a2a.TaskStateCanceled
			return pbconv.ToProtoTask(task)
		}
	}
	return nil, status.Error(codes.NotFound, "task not found")
}

func released(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func checkIdentity(ctx context.Context) error {
	values, _ := metadata.FromIncomingContext(ctx)
	for name, want := range map[string]string{"x-agent-name": "team__NS__workflow", "x-user-id": "reader", "authorization": "Bearer placeholder"} {
		if got := values.Get(name); len(got) != 1 || got[0] != want {
			return status.Errorf(codes.Unauthenticated, "invalid %s (%d values)", name, len(got))
		}
	}
	return nil
}

func runWorkflow(ctx context.Context, client *controllerclient.Client, nodes, edges string) ([]*session.Event, error) {
	reg := declarative.NewRegistry()
	reg.SetNativeAgentRunner(nativeagent.NewRunner(client))
	spec, err := reg.Parse([]byte(fmt.Sprintf(`
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: root
    agent_class: Workflow
    nodes:
%s
    edges:
%s
`, nodes, edges)))
	if err != nil {
		return nil, err
	}
	system, err := declarative.Build(ctx, spec, reg)
	if err != nil {
		return nil, err
	}
	svc := session.InMemoryService()
	created, err := svc.Create(ctx, &session.CreateRequest{AppName: "test", UserID: "reader"})
	if err != nil {
		return nil, err
	}
	r, err := runner.New(runner.Config{AppName: "test", Agent: system.Root(), SessionService: svc})
	if err != nil {
		return nil, err
	}
	var events []*session.Event
	for event, err := range r.Run(ctx, "reader", created.Session.ID(), genai.NewContentFromText("hello", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
	return events, nil
}
