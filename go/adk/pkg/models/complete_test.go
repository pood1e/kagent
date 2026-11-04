package models

import (
	"context"
	"encoding/json"
	"iter"
	"testing"

	"github.com/openai/openai-go/v3"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

type scriptedLLM struct{ responses []*model.LLMResponse }

func (scriptedLLM) Name() string { return "scripted" }

func (s scriptedLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for _, resp := range s.responses {
			if !yield(resp, nil) {
				return
			}
		}
	}
}

func generate(t *testing.T, llm model.LLM) []*model.LLMResponse {
	t.Helper()
	var out []*model.LLMResponse
	for resp, err := range llm.GenerateContent(t.Context(), &model.LLMRequest{}, true) {
		if err != nil {
			t.Fatalf("GenerateContent() error = %v", err)
		}
		out = append(out, resp)
	}
	return out
}

func TestRequireCompleteOutputFailsIncompleteTurns(t *testing.T) {
	for _, reason := range []genai.FinishReason{genai.FinishReasonMaxTokens, genai.FinishReasonMalformedFunctionCall} {
		t.Run(string(reason), func(t *testing.T) {
			partial := &model.LLMResponse{Partial: true, Content: genai.NewContentFromText("draft", genai.RoleModel)}
			final := &model.LLMResponse{
				TurnComplete: true,
				FinishReason: reason,
				Content: &genai.Content{Role: string(genai.RoleModel), Parts: []*genai.Part{
					{Text: "draft"},
					genai.NewPartFromFunctionCall("save_note", map[string]any{}),
				}},
			}
			got := generate(t, RequireCompleteOutput(scriptedLLM{responses: []*model.LLMResponse{partial, final}}))
			if len(got) != 2 || got[0] != partial {
				t.Fatalf("responses = %#v, want the partial response unchanged and a final response", got)
			}
			if got[1].ErrorCode != string(reason) || got[1].ErrorMessage == "" {
				t.Fatalf("final error = %q %q, want code %q with a message", got[1].ErrorCode, got[1].ErrorMessage, reason)
			}
			parts := got[1].Content.Parts
			if len(parts) != 1 || parts[0].Text != "draft" {
				t.Fatalf("final parts = %#v, want only the text part", parts)
			}
			if final.ErrorCode != "" || len(final.Content.Parts) != 2 {
				t.Fatal("the provider response was modified")
			}
		})
	}
}

func TestRequireCompleteOutputKeepsCompletedTurns(t *testing.T) {
	final := &model.LLMResponse{TurnComplete: true, FinishReason: genai.FinishReasonStop, Content: genai.NewContentFromText("done", genai.RoleModel)}
	got := generate(t, RequireCompleteOutput(scriptedLLM{responses: []*model.LLMResponse{final}}))
	if len(got) != 1 || got[0] != final {
		t.Fatalf("responses = %#v, want the completed response unchanged", got)
	}
}

func TestChatCompletionReportsMalformedFunctionCalls(t *testing.T) {
	for _, tc := range []struct {
		finish string
		want   genai.FinishReason
	}{
		{finish: "tool_calls", want: genai.FinishReasonMalformedFunctionCall},
		{finish: "length", want: genai.FinishReasonMaxTokens},
	} {
		t.Run(tc.finish, func(t *testing.T) {
			raw := `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,
				"finish_reason":"` + tc.finish + `","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function",
				"function":{"name":"add_commit","arguments":"{\"files\":[{\"path\":"}}]}}]}`
			var completion openai.ChatCompletion
			if err := json.Unmarshal([]byte(raw), &completion); err != nil {
				t.Fatalf("json.Unmarshal(ChatCompletion) error = %v", err)
			}
			if got := chatCompletionToLLMResponse(&completion).FinishReason; got != tc.want {
				t.Fatalf("FinishReason = %q, want %q", got, tc.want)
			}
		})
	}
}
