package database

import runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"

func personalMemoryScope(agent string) *runtimev1.MemoryScope {
	return &runtimev1.MemoryScope{Target: &runtimev1.MemoryScope_AgentName{AgentName: agent}}
}
