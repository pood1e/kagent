package taskcatalog

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCatalogCursorRejectsMalformedAndUnboundPositions(t *testing.T) {
	encode := func(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
	for _, token := range []string{
		"%invalid", strings.Repeat("a", 2049), encode(`{}`),
		encode(`{"scope":"other","id":"task","at":"2026-11-01T00:00:00Z"}`),
		encode(`{"scope":"binding","id":"task","at":"2026-11-01T00:00:00Z","extra":true}`),
		encode(`{"scope":"binding","id":"task","at":"2026-11-01T00:00:00Z"}{}`),
	} {
		_, _, err := readCursor(token, "binding")
		require.Error(t, err)
	}
	at := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := nextCursor("binding", "task", &at)
	require.Error(t, err, "encoding failures cannot silently return an empty continuation")
}
