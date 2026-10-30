package declarative

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

const nativeDocument = `
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: root
    agent_class: Workflow
    entry: call
    nodes:
      - id: call
        type: agent
        agent_ref: {namespace: team, name: analyst}
        output_schema: {type: string}
`

func TestNativeAgentValidation(t *testing.T) {
	for _, tt := range []struct{ name, document, want string }{
		{"valid", nativeDocument, ""},
		{"default namespace", strings.ReplaceAll(nativeDocument, "namespace: team, ", ""), ""},
		{"empty target", strings.ReplaceAll(nativeDocument, "{namespace: team, name: analyst}", "{}"), "agent_ref"},
		{"invalid name", strings.ReplaceAll(nativeDocument, "name: analyst", "name: Bad_Name"), "agent_ref"},
		{"invalid namespace", strings.ReplaceAll(nativeDocument, "namespace: team", "namespace: Bad_Name"), "agent_ref"},
		{"two targets", strings.ReplaceAll(nativeDocument, "type: agent", "type: agent\n        agent: local"), "exactly one"},
		{"missing target", strings.ReplaceAll(nativeDocument, "agent_ref: {namespace: team, name: analyst}", ""), "exactly one"},
		{"wrong node type", strings.ReplaceAll(nativeDocument, "type: agent", "type: cel"), "agent_ref"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.document))
			if tt.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.want)
			}
		})
	}
	_, err := NewRegistry().Parse([]byte(nativeDocument))
	require.ErrorContains(t, err, "native Agent execution is not configured")
}

func TestNativeParallelUsesEngineRetryIsolationAndContracts(t *testing.T) {
	reg := NewRegistry()
	var mu sync.Mutex
	attempts := map[string]int{}
	var active, peak atomic.Int32
	ready := make(chan struct{})
	var once sync.Once
	reg.SetNativeAgentRunner(func(ctx agent.Context, ref *apiv1.ResourceReference, input any, emit func(*session.Event) error) (any, error) {
		require.Equal(t, "analyst", ref.Name)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		if n == 2 {
			once.Do(func() { close(ready) })
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ready:
		}
		mu.Lock()
		attempts[ctx.Branch()]++
		attempt := attempts[ctx.Branch()]
		mu.Unlock()
		event := session.NewEvent(ctx, ctx.InvocationID())
		event.Content = genai.NewContentFromText("native progress", genai.RoleModel)
		if err := emit(event); err != nil {
			return nil, err
		}
		if attempt == 1 {
			return nil, errors.New("ambiguous transport")
		}
		return map[string]any{"value": input}, nil
	})
	spec, err := reg.Parse([]byte(`
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: root
    agent_class: Workflow
    nodes:
      - {id: split, type: cel, output: "['same', 'same', 'same', 'same']"}
      - id: call
        type: parallel_worker
        agent_ref: {namespace: team, name: analyst}
        max_concurrency: 2
        input_schema: {type: string}
        output_schema: {type: object, required: [value], properties: {value: {type: string}}}
        retry: {max_attempts: 2, initial_delay: 0s}
      - {id: finish, type: cel, output: 'string(input.size())'}
    edges:
      - {from: START, to: split}
      - {from: split, to: call}
      - {from: call, to: finish}
`))
	require.NoError(t, err)
	system, err := Build(t.Context(), spec, reg)
	require.NoError(t, err)
	svc, id := newSession(t)
	events, err := tryRun(t, system, svc, id, "run")
	require.NoError(t, err)
	values := outputs(events)
	require.Equal(t, "4", values[len(values)-1])
	require.EqualValues(t, 2, peak.Load())
	require.Len(t, attempts, 4, "duplicate inputs retain distinct per-item identities")
	for _, count := range attempts {
		require.Equal(t, 2, count)
	}
	var progress int
	for _, event := range events {
		if event.Content != nil && len(event.Content.Parts) > 0 && event.Content.Parts[0].Text == "native progress" {
			progress++
		}
	}
	require.Equal(t, 8, progress)
}

func TestNativeAgentOutputContract(t *testing.T) {
	reg := NewRegistry()
	reg.SetNativeAgentRunner(func(agent.Context, *apiv1.ResourceReference, any, func(*session.Event) error) (any, error) {
		return 42, nil
	})
	spec, err := reg.Parse([]byte(nativeDocument))
	require.NoError(t, err)
	system, err := Build(t.Context(), spec, reg)
	require.NoError(t, err)
	svc, id := newSession(t)
	_, err = tryRun(t, system, svc, id, "run")
	require.ErrorContains(t, err, "output_schema")
}
