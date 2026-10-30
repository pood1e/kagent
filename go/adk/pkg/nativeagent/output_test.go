package nativeagent

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/stretchr/testify/require"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
)

func TestTaskOutputUsesStructuredResultOrFinalVisibleArtifact(t *testing.T) {
	for _, tt := range []struct {
		name  string
		parts a2a.ContentParts
		want  any
	}{
		{"structured", a2a.ContentParts{apia2a.NewStructuredOutputPart(map[string]any{"ok": true}, "schema-digest")}, map[string]any{"ok": true}},
		{"json", a2a.ContentParts{a2a.NewTextPart(`{"ok":true}`)}, map[string]any{"ok": true}},
		{"text", a2a.ContentParts{a2a.NewTextPart("final answer")}, "final answer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			value, err := taskOutput(&a2a.Task{ID: "task", Artifacts: []*a2a.Artifact{
				{ID: "earlier", Parts: a2a.ContentParts{a2a.NewTextPart("intermediate tool and reasoning output")}},
				{ID: "final", Parts: tt.parts},
			}})
			require.NoError(t, err)
			require.Equal(t, tt.want, value)
		})
	}
	_, err := taskOutput(&a2a.Task{ID: "empty"})
	require.ErrorContains(t, err, "completed without output")
}
