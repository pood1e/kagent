package a2a

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/text/language"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
)

type localeContext struct {
	adkagent.Context
	values context.Context
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
		if len(parts) != 2 || !strings.Contains(parts[1].Text, "Response locale: "+tag.String()) || !strings.Contains(parts[1].Text, "Authorization and safety requirements remain unchanged") {
			t.Fatalf("locale instruction = %#v", parts)
		}
	}
}
