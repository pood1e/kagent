package a2a

import (
	"context"
	"fmt"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"golang.org/x/text/language"
)

// ResponseLocaleMetadataKey records the response language without changing task input.
const ResponseLocaleMetadataKey = MetadataPrefix + "response-locale"

type responseLocaleKey struct{}

// ParseAcceptLanguage selects the highest-priority non-wildcard BCP 47 locale.
func ParseAcceptLanguage(value string) (language.Tag, error) {
	if value == "" {
		return language.Und, nil
	}
	if len(value) > 256 {
		return language.Und, fmt.Errorf("accept-language exceeds 256 bytes")
	}
	tags, quality, err := language.ParseAcceptLanguage(value)
	if err != nil {
		return language.Und, fmt.Errorf("invalid accept-language: %w", err)
	}
	for i, tag := range tags {
		if tag != language.Und && tag.String() != "mul" && quality[i] > 0 {
			return tag, nil
		}
	}
	return language.Und, nil
}

// WithResponseLocale carries the accepted task preference through runtime calls.
func WithResponseLocale(ctx context.Context, tag language.Tag) context.Context {
	return context.WithValue(ctx, responseLocaleKey{}, tag)
}

// ContextResponseLocale returns Und when the invocation has no language preference.
func ContextResponseLocale(ctx context.Context) language.Tag {
	tag, _ := ctx.Value(responseLocaleKey{}).(language.Tag)
	return tag
}

// SetResponseLocale attaches a canonical locale to an A2A metadata carrier.
func SetResponseLocale(carrier a2atype.MetadataCarrier, tag language.Tag) {
	if tag != language.Und {
		carrier.SetMeta(ResponseLocaleMetadataKey, tag.String())
	}
}

// ResponseLocale reads and validates the persisted response language.
func ResponseLocale(carrier a2atype.MetadataCarrier) (language.Tag, error) {
	value, exists := carrier.Meta()[ResponseLocaleMetadataKey]
	if !exists {
		return language.Und, nil
	}
	text, ok := value.(string)
	if !ok || text == "" || len(text) > 128 {
		return language.Und, fmt.Errorf("response locale must be a BCP 47 language tag")
	}
	tag, err := language.Parse(text)
	if err != nil || tag == language.Und {
		return language.Und, fmt.Errorf("invalid response locale %q", text)
	}
	return tag, nil
}

// TaskResponseLocale retains the opening message's preference across task resumes.
func TaskResponseLocale(task *a2atype.Task) (language.Tag, error) {
	if task != nil {
		for _, message := range task.History {
			if message != nil && message.Role == a2atype.MessageRoleUser {
				return ResponseLocale(message)
			}
		}
	}
	return language.Und, nil
}
