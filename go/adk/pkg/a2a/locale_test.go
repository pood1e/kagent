package a2a

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
)

type localeContext struct {
	adkagent.Context
	values context.Context
}

func TestResponseLocaleReachesTheModelThroughExecutor(t *testing.T) {
	const input = `{"incidentId":"incident-1","request":"核验"}`
	for _, tag := range []language.Tag{language.English, language.Chinese, language.Und} {
		t.Run(tag.String(), func(t *testing.T) {
			recorder := &geminiAPIRecorder{}
			root, err := llmagent.New(llmagent.Config{
				Name: "inspector", Instruction: "Original safety policy", Model: recorder,
			})
			if err != nil {
				t.Fatal(err)
			}
			executor, err := NewKAgentExecutor(KAgentExecutorConfig{
				AppName: "test", SessionService: adksession.InMemoryService(),
				Logger:       slog.New(slog.DiscardHandler),
				RunnerConfig: runner.Config{AppName: "test", Agent: root},
			})
			if err != nil {
				t.Fatal(err)
			}
			message := a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart(input))
			apia2a.SetResponseLocale(message, tag)
			request := &a2asrv.ExecutorContext{TaskID: "task-1", ContextID: "context-1", Message: message}
			for _, err := range executor.Execute(t.Context(), request) {
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(recorder.requests) != 1 {
				t.Fatalf("model called %d times, want 1", len(recorder.requests))
			}
			captured := recorder.requests[0]
			if texts := userTexts(captured); len(texts) != 1 || strings.TrimSpace(texts[0]) != input {
				t.Fatalf("model input = %q, want unchanged structured input", texts)
			}
			var instruction strings.Builder
			for _, part := range captured.Config.SystemInstruction.Parts {
				instruction.WriteString(part.Text)
			}
			if !strings.Contains(instruction.String(), "Original safety policy") {
				t.Fatal("original instruction was lost")
			}
			if tag == language.Und {
				if strings.Contains(instruction.String(), "interface language") {
					t.Fatal("unspecified locale changed the model instruction")
				}
			} else if !strings.Contains(instruction.String(), "selected "+display.English.Tags().Name(tag)+" ("+tag.String()+")") {
				t.Fatalf("model instruction is missing the selected locale: %s", instruction.String())
			}
		})
	}
}

func (c localeContext) Value(key any) any { return c.values.Value(key) }

func TestResponseLocalePluginPreservesTemplateAndStructuredInput(t *testing.T) {
	plug, err := responseLocalePlugin()
	if err != nil {
		t.Fatal(err)
	}
	template := &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText("Original safety policy", "system")}
	input := genai.NewContentFromText(`{"incidentId":"incident-1"}`, "user")
	for _, tag := range []language.Tag{language.English, language.Chinese, language.Und} {
		request := &model.LLMRequest{Config: template, Contents: []*genai.Content{input}}
		ctx := localeContext{values: apia2a.WithResponseLocale(t.Context(), tag)}
		if _, err := plug.BeforeModelCallback()(ctx, request); err != nil {
			t.Fatal(err)
		}
		if len(template.SystemInstruction.Parts) != 1 || request.Contents[0] != input {
			t.Fatal("shared template or structured task input changed")
		}
		if tag == language.Und {
			if request.Config != template {
				t.Fatal("unspecified locale changed the request")
			}
			continue
		}
		parts := request.Config.SystemInstruction.Parts
		if len(parts) != 2 || !strings.Contains(parts[1].Text, "selected "+display.English.Tags().Name(tag)+" ("+tag.String()+")") ||
			!strings.Contains(parts[1].Text, "the language of their message alone is not such a request") ||
			!strings.Contains(parts[1].Text, "Authorization and safety requirements remain unchanged") {
			t.Fatalf("locale instruction = %#v", parts)
		}
	}
}
