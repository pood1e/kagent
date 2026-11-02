package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type accessStub struct {
	verb auth.Verb
	ref  types.NamespacedName
	err  error
}

func (s *accessStub) Authorize(_ context.Context, verb auth.Verb, ref types.NamespacedName) error {
	s.verb, s.ref = verb, ref
	return s.err
}

type admissionStub struct{ calls int }

func (s *admissionStub) Workflow(context.Context, *v1alpha3.Agent, *v1alpha3.AgentTemplate, []byte) (string, error) {
	s.calls++
	return "candidate-revision", nil
}
func (s *admissionStub) WorkflowRevision(context.Context, types.NamespacedName) (string, error) {
	s.calls++
	return "published-revision", nil
}

type sessionStub struct{ err error }

func (s sessionStub) Get(context.Context, string) (*apiv1.Session, error) {
	return &apiv1.Session{PreparedRevision: "pinned"}, s.err
}

type revisionStub struct {
	requested string
	snapshot  json.RawMessage
}

func (s *revisionStub) GetRuntimeRevision(_ context.Context, revision string) (*database.RuntimeRevision, error) {
	s.requested = revision
	return &database.RuntimeRevision{SourceSnapshot: s.snapshot}, nil
}

func TestCompilerAccessPrecedesDependencyReads(t *testing.T) {
	access := &accessStub{err: errors.New("denied")}
	compiler := &admissionStub{}
	service := NewService(access, compiler, nil, nil)
	ref := types.NamespacedName{Namespace: "team", Name: "graph"}
	agent := &v1alpha3.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: ref.Namespace, Name: ref.Name}}
	if _, err := service.Validate(t.Context(), agent, nil, []byte("candidate")); !errors.Is(err, access.err) {
		t.Fatalf("Validate() error = %v, want denial", err)
	}
	if access.verb != auth.VerbCreate || access.ref != ref || compiler.calls != 0 {
		t.Fatalf("candidate access = %s %v, compiler calls = %d", access.verb, access.ref, compiler.calls)
	}
	if _, err := service.Revision(t.Context(), ref); !errors.Is(err, access.err) {
		t.Fatalf("Revision() error = %v, want denial", err)
	}
	if access.verb != auth.VerbGet || access.ref != ref || compiler.calls != 0 {
		t.Fatalf("published access = %s %v, compiler calls = %d", access.verb, access.ref, compiler.calls)
	}
	access.err = nil
	if revision, err := service.Revision(t.Context(), ref); err != nil || revision != "published-revision" {
		t.Fatalf("Revision() = %q, %v", revision, err)
	}
}

func TestSessionDefinitionUsesOnlyItsAuthorizedPinnedRevision(t *testing.T) {
	document := &configv1.ADKWorkflowConfig{Document: []byte("original document"), Sha256: "original-sha"}
	snapshot, err := json.Marshal(struct {
		Workflow *configv1.ADKWorkflowConfig `json:"workflow"`
	}{document})
	if err != nil {
		t.Fatal(err)
	}
	revisions := &revisionStub{snapshot: snapshot}
	denial := serviceerrors.NewPermissionDenied("Session denied", nil)
	service := NewService(nil, nil, sessionStub{err: denial}, revisions)
	if _, err := service.Session(t.Context(), "session"); !errors.Is(err, denial) || revisions.requested != "" {
		t.Fatalf("denied Session: error = %v, revision read = %q", err, revisions.requested)
	}
	service.sessions = sessionStub{}
	result, err := service.Session(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if result.PreparedRevision != "pinned" || revisions.requested != "pinned" || string(result.Document) != string(document.Document) || result.Sha256 != document.Sha256 {
		t.Fatalf("Session definition = %v, requested = %q", result, revisions.requested)
	}
	for _, test := range []struct {
		snapshot string
		code     serviceerrors.Code
	}{
		{`{"inputs":[]}`, serviceerrors.CodeNotFound},
		{`broken`, serviceerrors.CodeInternal},
	} {
		revisions.snapshot = json.RawMessage(test.snapshot)
		if _, err := service.Session(t.Context(), "session"); serviceerrors.CodeOf(err) != test.code {
			t.Fatalf("snapshot %q error = %v, want %s", test.snapshot, err, test.code)
		}
	}
}
