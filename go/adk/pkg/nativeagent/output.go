package nativeagent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
)

func taskOutput(task *a2a.Task) (any, error) {
	for _, artifact := range slices.Backward(task.Artifacts) {
		for _, part := range slices.Backward(artifact.Parts) {
			if apia2a.IsStructuredOutputPart(part) {
				return part.Data(), nil
			}
		}
	}
	// Earlier artifacts can contain tool activity and reasoning. The last
	// visible text artifact is the native Agent's final response.
	for _, artifact := range slices.Backward(task.Artifacts) {
		if text := partsText(artifact.Parts); text != "" {
			return decodeText(text), nil
		}
	}
	if task.Status.Message != nil {
		if text := partsText(task.Status.Message.Parts); text != "" {
			return decodeText(text), nil
		}
	}
	for _, message := range slices.Backward(task.History) {
		if message.Role == a2a.MessageRoleAgent {
			if text := partsText(message.Parts); text != "" {
				return decodeText(text), nil
			}
		}
	}
	return nil, fmt.Errorf("native Agent task %s completed without output", task.ID)
}

func partsText(parts a2a.ContentParts) string {
	var text strings.Builder
	for _, part := range parts {
		if part != nil {
			text.WriteString(part.Text())
		}
	}
	return text.String()
}

func decodeText(text string) any {
	var output any
	if json.Unmarshal([]byte(text), &output) == nil {
		return output
	}
	return text
}
