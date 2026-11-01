package database

import (
	"fmt"
	"slices"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

type taskCatalogMetadata struct {
	InitialPrompt       string
	PendingToolApproval bool
	PendingUserInput    bool
	ToolCallCount       int64
}

// projectTaskCatalog indexes one public task with its published message history.
func projectTaskCatalog(task *a2a.Task) taskCatalogMetadata {
	var result taskCatalogMetadata
	for _, message := range task.History {
		if message.Role == a2a.MessageRoleUser {
			var parts []string
			for _, part := range message.Parts {
				if part != nil {
					if _, ok := part.Content.(a2a.Text); ok {
						parts = append(parts, part.Text())
					}
				}
			}
			result.InitialPrompt = strings.TrimSpace(strings.Join(parts, "\n"))
			break
		}
	}
	request := catalogApprovalRequest(task)
	if request != nil {
		tools := request.Tools
		if request.Nested != nil && len(request.Nested.Tools) > 0 {
			tools = request.Nested.Tools
		}
		tools = slices.DeleteFunc(slices.Clone(tools), func(tool *apiv1.HitlTool) bool {
			return tool.GetId() == "" || tool.GetName() == ""
		})
		result.PendingToolApproval = len(tools) > 0
		for _, message := range task.History {
			if catalogHITLKind(message) != apia2a.HITLTypeToolApprovalResponse {
				continue
			}
			response, err := apia2a.ParseToolApprovalResponse(message)
			if err != nil {
				continue
			}
			if len(tools) > 0 && !slices.ContainsFunc(tools, func(tool *apiv1.HitlTool) bool {
				return !slices.ContainsFunc(response.Approvals, func(answer *apiv1.ToolApproval) bool {
					return answer.GetId() == tool.GetId()
				})
			}) {
				result.PendingToolApproval = false
				break
			}
		}
	}
	question := catalogQuestionRequest(task)
	if question != nil {
		result.PendingUserInput = true
		for _, message := range task.History {
			if catalogHITLKind(message) != apia2a.HITLTypeAskUserResponse {
				continue
			}
			response, err := apia2a.ParseAskUserResponse(message)
			if err == nil && response.Id == question.Id {
				result.PendingUserInput = false
				break
			}
		}
	}
	trusted, untrusted := map[string]struct{}{}, map[string]struct{}{}
	for _, message := range task.History {
		keys := catalogToolKeys(message.Parts, "message:"+message.ID)
		if message.Role != a2a.MessageRoleAgent {
			for _, key := range keys {
				untrusted[key] = struct{}{}
			}
		} else if catalogHITLKind(message) == "" {
			for _, key := range keys {
				trusted[key] = struct{}{}
			}
		}
	}
	for _, artifact := range task.Artifacts {
		for _, key := range catalogToolKeys(artifact.Parts, "artifact:"+string(artifact.ID)) {
			trusted[key] = struct{}{}
		}
	}
	for key := range trusted {
		if _, blocked := untrusted[key]; !blocked {
			result.ToolCallCount++
		}
	}
	return result
}

func catalogApprovalRequest(task *a2a.Task) *apiv1.ToolApprovalRequest {
	if task.Status.State != a2a.TaskStateInputRequired {
		return nil
	}
	request, err := apia2a.ParseToolApprovalRequest(task.Status.Message)
	if err != nil {
		return nil // Invalid extension payloads do not establish a human interaction.
	}
	return request
}

func catalogQuestionRequest(task *a2a.Task) *apiv1.AskUserRequest {
	if task.Status.State != a2a.TaskStateInputRequired {
		return nil
	}
	request, err := apia2a.ParseAskUserRequest(task.Status.Message)
	if err != nil {
		return nil
	}
	return request
}

func catalogHITLKind(message *a2a.Message) string {
	if message == nil || !slices.Contains(message.Extensions, apia2a.HITLExtensionURI) {
		return ""
	}
	payload, _ := message.Metadata[apia2a.HITLExtensionURI].(map[string]any)
	kind, _ := payload["type"].(string)
	switch kind {
	case apia2a.HITLTypeToolApprovalRequest, apia2a.HITLTypeToolApprovalResponse,
		apia2a.HITLTypeAskUserRequest, apia2a.HITLTypeAskUserResponse:
		return kind
	default:
		return ""
	}
}

func catalogToolKeys(parts a2a.ContentParts, source string) []string {
	var keys []string
	for index, part := range parts {
		activity, ok := apia2a.ParseToolActivity(part)
		if !ok {
			continue
		}
		key := activity.ID
		if key == "" {
			key = fmt.Sprintf("unpaired:%s:%d:%s", source, index, activity.Kind)
		}
		keys = append(keys, key)
	}
	return keys
}

func catalogArtifactTools(task *a2a.Task) []string {
	var keys []string
	if task != nil {
		for _, artifact := range task.Artifacts {
			keys = append(keys, catalogToolKeys(artifact.Parts, "artifact:"+string(artifact.ID))...)
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}
