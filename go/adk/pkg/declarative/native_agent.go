package declarative

import (
	"fmt"

	"buf.build/go/protovalidate"
	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/protobuf/proto"
)

func (v *validator) nativeAgent(path string, n *NodeSpec) {
	if n.AgentRef == nil {
		if n.Type == NodeAgent && len(n.OutputSchema) > 0 {
			v.addf(path+".output_schema", "requires agent_ref; local agents declare their own output_schema")
		}
		return
	}
	ref := proto.CloneOf(n.AgentRef)
	if ref.Namespace == "" {
		ref.Namespace = "default"
	}
	if err := protovalidate.Validate(ref); err != nil {
		v.addf(path+".agent_ref", "%v", err)
	}
	if v.refs != nil && v.refs.nativeRunner == nil {
		v.addf(path+".agent_ref", "native Agent execution is not configured")
	}
}

func (b *builder) nativeAgentNode(id string, n *NodeSpec, in, out *jsonschema.Schema, cfg workflow.NodeConfig, render bool) (workflow.Node, error) {
	var output *jsonschema.Resolved
	if out != nil {
		var err error
		output, err = out.Resolve(nil)
		if err != nil {
			return nil, err
		}
	}
	return workflow.NewEmittingFunctionNodeWithSchema(id, func(ctx agent.Context, input any, emit func(*session.Event) error) (any, error) {
		// ParallelWorker runs its wrapped node directly, outside the scheduler's
		// output validation. Enforce the per-item contract at the node boundary.
		value, err := b.reg.nativeRunner(ctx, n.AgentRef, input, emit)
		if err != nil {
			return nil, err
		}
		if output != nil {
			if err := output.Validate(value); err != nil {
				return nil, fmt.Errorf("native Agent %s/%s output_schema: %w", n.AgentRef.Namespace, n.AgentRef.Name, err)
			}
		}
		return finish(ctx, emit, value, render)
	}, in, out, cfg)
}
