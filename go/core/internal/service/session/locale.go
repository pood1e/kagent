package session

import (
	"context"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"golang.org/x/text/language"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
)

func captureResponseLocale(ctx context.Context, request *a2atype.SendMessageRequest) error {
	if request == nil || request.Message == nil {
		return nil
	}
	tag, err := apia2a.ResponseLocale(request.Message)
	if err != nil {
		return a2atype.NewError(a2atype.ErrInvalidParams, err.Error())
	}
	if tag == language.Und && request.Message.TaskID == "" {
		if call, ok := a2asrv.CallContextFrom(ctx); ok && call.ServiceParams() != nil {
			if values, ok := call.ServiceParams().Get("accept-language"); ok {
				for _, value := range values {
					tag, err = apia2a.ParseAcceptLanguage(value)
					if err != nil {
						return a2atype.NewError(a2atype.ErrInvalidParams, err.Error())
					}
					if tag != language.Und {
						break
					}
				}
			}
		}
	}
	apia2a.SetResponseLocale(request.Message, tag)
	return nil
}
