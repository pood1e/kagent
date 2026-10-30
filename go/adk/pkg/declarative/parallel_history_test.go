package declarative

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/adk/v2/agent"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
)

type boundedToolCaller struct {
	toolCaller
	calls atomic.Int32
}

func (m *boundedToolCaller) GenerateContent(ctx context.Context, req *adkmodel.LLMRequest, stream bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	if m.calls.Add(1) > 8 {
		return func(yield func(*adkmodel.LLMResponse, error) bool) {
			yield(nil, errors.New("tool response is missing from the model history"))
		}
	}
	return m.toolCaller.GenerateContent(ctx, req, stream)
}

func TestParallelWorkerPersistsMCPRoundTrips(t *testing.T) {
	for _, concurrency := range []int{1, 2} {
		t.Run(fmt.Sprintf("concurrency_%d", concurrency), func(t *testing.T) {
			crm := startCRM(t)
			t.Setenv("CRM_MCP_URL", crm.URL)
			t.Setenv("CRM_API_KEY", crmKey)
			model := &boundedToolCaller{toolCaller: toolCaller{tool: "lookup_customer"}}
			reg := NewRegistry()
			require.NoError(t, reg.RegisterModel("caller", model))
			sys := buildDoc(t, reg, head+crmServers+fmt.Sprintf(`
agents:
  - name: root
    agent_class: Workflow
    entry: items
    nodes:
      - { id: items, type: cel, output: "['c-42', 'c-13']" }
      - { id: each, type: parallel_worker, agent: analyst, max_concurrency: %d }
      - { id: done, type: cel, output: "toJson(input)" }
    edges:
      - { from: items, to: each }
      - { from: each, to: done }
  - { name: analyst, agent_class: LlmAgent, model: caller, instruction: Look up the customer, tools: [crm] }
`, concurrency))
			svc, id := newSession(t)
			events, err := tryRun(t, sys, svc, id, "start")
			require.NoError(t, err)
			require.Contains(t, replyText(events), "c-42")
			require.Contains(t, replyText(events), "c-13")
			require.EqualValues(t, 2, crm.calls.Load())
			require.EqualValues(t, 4, model.calls.Load())

			saved, err := svc.Get(t.Context(), &session.GetRequest{AppName: "test", UserID: "user", SessionID: id})
			require.NoError(t, err)
			branches := map[string]int{}
			for event := range saved.Session.Events().All() {
				if event.Content == nil {
					continue
				}
				for _, part := range event.Content.Parts {
					if part.FunctionResponse != nil {
						branches[event.Branch]++
					}
				}
			}
			require.Len(t, branches, 2, "each item retains its own tool history")
			for _, responses := range branches {
				require.Equal(t, 1, responses)
			}
		})
	}
}

func TestParallelWorkerRetriesOnlyTheFailedItem(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	reg := NewRegistry()
	require.NoError(t, reg.RegisterFunction("item", func(_ agent.Context, input any) (Result, error) {
		key := input.(string)
		mu.Lock()
		defer mu.Unlock()
		calls[key]++
		if key == "b" && calls[key] == 1 {
			return Result{}, errors.New("temporary failure")
		}
		return Output(key), nil
	}))
	sys := buildDoc(t, reg, head+`
agents:
  - name: root
    agent_class: Workflow
    entry: items
    nodes:
      - { id: items, type: cel, output: "['a', 'b', 'c']" }
      - id: each
        type: parallel_worker
        function: item
        max_concurrency: 2
        retry: { max_attempts: 2, initial_delay: 1ms, max_delay: 1ms, jitter: 0 }
      - { id: done, type: cel, output: "toJson(input)" }
    edges:
      - { from: items, to: each }
      - { from: each, to: done }
`)
	svc, id := newSession(t)
	events, err := tryRun(t, sys, svc, id, "start")
	require.NoError(t, err)
	require.Contains(t, replyText(events), `["a","b","c"]`)
	require.Equal(t, map[string]int{"a": 1, "b": 2, "c": 1}, calls)
}

func TestParallelWorkerFailureCancelsOtherItems(t *testing.T) {
	started := make(chan struct{})
	var cancelled, extra atomic.Bool
	failed := errors.New("item failed")
	reg := NewRegistry()
	require.NoError(t, reg.RegisterFunction("item", func(ctx agent.Context, input any) (Result, error) {
		switch input {
		case "fail":
			select {
			case <-started:
				return Result{}, failed
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		case "wait":
			close(started)
			<-ctx.Done()
			cancelled.Store(true)
			return Result{}, ctx.Err()
		default:
			extra.Store(true)
			return Output(input), nil
		}
	}))
	sys := buildDoc(t, reg, head+`
agents:
  - name: root
    agent_class: Workflow
    entry: items
    nodes:
      - { id: items, type: cel, output: "['fail', 'wait', 'extra']" }
      - { id: each, type: parallel_worker, function: item, max_concurrency: 2, timeout: 3s }
      - { id: done, type: cel, output: "toJson(input)" }
    edges:
      - { from: items, to: each }
      - { from: each, to: done }
`)
	svc, id := newSession(t)
	_, err := tryRun(t, sys, svc, id, "start")
	require.ErrorIs(t, err, failed)
	require.True(t, cancelled.Load())
	require.False(t, extra.Load())
}
