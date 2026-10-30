package declarative

import (
	"iter"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
)

// parallelNode streams each item's events to the runner while the ADK worker
// owns concurrency, retry and ordered output aggregation. A worker alone drops
// intermediate events, leaving an LLM unable to read its own tool responses.
func parallelNode(name string, wrapped workflow.Node, concurrency int, cfg workflow.NodeConfig) workflow.Node {
	retry := cfg.RetryConfig
	cfg.RetryConfig = nil
	return workflow.NewEmittingFunctionNode(name, func(ctx agent.Context, input any, emit func(*session.Event) error) (any, error) {
		worker, err := workflow.NewParallelWorker(name, streamingNode{Node: wrapped, emit: emit}, concurrency, workflow.NodeConfig{RetryConfig: retry})
		if err != nil {
			return nil, err
		}
		for event, err := range worker.Run(ctx, input) {
			if err != nil {
				return nil, err
			}
			if event != nil {
				return event.Output, nil
			}
		}
		return nil, nil
	}, cfg)
}

type streamingNode struct {
	workflow.Node
	emit func(*session.Event) error
}

func (n streamingNode) Run(ctx agent.Context, input any) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for event, err := range n.Node.Run(ctx, input) {
			if err != nil {
				yield(nil, err)
				return
			}
			if event != nil {
				forwarded := *event
				// Only the worker's aggregate routes and produces node output.
				forwarded.Output, forwarded.Routes = nil, nil
				if event.NodeInfo != nil {
					info := *event.NodeInfo
					info.MessageAsOutput = false
					forwarded.NodeInfo = &info
				}
				if err := n.emit(&forwarded); err != nil {
					yield(nil, err)
					return
				}
			}
			if !yield(event, nil) {
				return
			}
		}
	}
}
