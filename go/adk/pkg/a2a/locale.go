package a2a

import (
	"fmt"
	"slices"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/genai"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
)

func responseLocalePlugin() (*plugin.Plugin, error) {
	return plugin.New(plugin.Config{
		Name: "kagent-response-locale",
		BeforeModelCallback: func(ctx adkagent.Context, request *model.LLMRequest) (*model.LLMResponse, error) {
			tag := apia2a.ContextResponseLocale(ctx)
			if tag == language.Und {
				return nil, nil
			}
			name := display.English.Tags().Name(tag)
			config := genai.GenerateContentConfig{}
			if request.Config != nil {
				config = *request.Config
			}
			instruction := genai.Content{}
			if config.SystemInstruction != nil {
				instruction = *config.SystemInstruction
			}
			instruction.Parts = append(slices.Clone(instruction.Parts), genai.NewPartFromText(fmt.Sprintf(
				"The user selected %s (%s) as the interface language. Write all user-facing explanations, questions, reports and delegated work in %s, even when the user message or role instructions are written in another language. Change the response language only when the user explicitly asks for a different output language; the language of their message alone is not such a request. Preserve identifiers, tool names, code, evidence quotations and structured field names or enum values exactly. Authorization and safety requirements remain unchanged.",
				name, tag, name)))
			config.SystemInstruction = &instruction
			request.Config = &config
			return nil, nil
		},
	})
}
