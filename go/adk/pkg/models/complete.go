package models

import (
	"context"
	"encoding/json"
	"iter"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// incompleteTurns describes the finish reasons of model turns that ended
// before the model completed them.
var incompleteTurns = map[genai.FinishReason]string{
	genai.FinishReasonMaxTokens:             "model output reached the output token limit before the turn completed",
	genai.FinishReasonMalformedFunctionCall: "model produced a function call whose arguments are not a JSON object",
}

// RequireCompleteOutput reports incomplete model turns as error responses.
//
// A turn cut off at the output token limit carries incomplete text and may
// carry function calls with truncated arguments, and a malformed function call
// has lost its arguments. Neither may settle a task or run a tool, so the
// final response gets an error code, keeps its text for diagnosis and drops
// its function calls. The agent loop then ends and the A2A task fails with the
// reason instead of completing with a fragment.
func RequireCompleteOutput(llm model.LLM) model.LLM {
	return completeOutputLLM{LLM: llm}
}

type completeOutputLLM struct{ model.LLM }

// GenerateContent implements model.LLM.
func (m completeOutputLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for resp, err := range m.LLM.GenerateContent(ctx, req, stream) {
			if err == nil && resp != nil && !resp.Partial {
				resp = requireComplete(resp)
			}
			if !yield(resp, err) {
				return
			}
		}
	}
}

func requireComplete(resp *model.LLMResponse) *model.LLMResponse {
	message, incomplete := incompleteTurns[resp.FinishReason]
	if !incomplete || resp.ErrorCode != "" {
		return resp
	}
	failed := *resp
	failed.ErrorCode = string(resp.FinishReason)
	failed.ErrorMessage = message
	failed.Content = withoutFunctionCalls(resp.Content)
	return &failed
}

func withoutFunctionCalls(content *genai.Content) *genai.Content {
	if content == nil {
		return nil
	}
	parts := make([]*genai.Part, 0, len(content.Parts))
	for _, part := range content.Parts {
		if part != nil && part.FunctionCall == nil {
			parts = append(parts, part)
		}
	}
	return &genai.Content{Role: content.Role, Parts: parts}
}

// decodeFunctionArgs decodes the JSON arguments of a model function call. ok
// is false when the arguments are not a JSON object.
func decodeFunctionArgs(raw string) (args map[string]any, ok bool) {
	if raw == "" {
		return nil, true
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, false
	}
	return args, true
}

// functionCallFinish reports malformed function calls through the finish
// reason. A turn that stopped at the token limit keeps that reason, which
// explains its broken arguments.
func functionCallFinish(reason genai.FinishReason, malformed bool) genai.FinishReason {
	if malformed && reason != genai.FinishReasonMaxTokens {
		return genai.FinishReasonMalformedFunctionCall
	}
	return reason
}
