package database

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func TestToolInventoryFollowsItsRevision(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	_, err := client.GetToolInventory(t.Context(), "revision")
	require.ErrorIs(t, err, ErrNotFound)
	catalog := &apiv1.BuiltinToolCatalog{Revision: "revision", ObservedAt: timestamppb.Now(), Tools: []*apiv1.DiscoveredTool{{Name: "read_file", Description: "Read files"}}}
	require.NoError(t, client.SaveToolInventory(t.Context(), catalog))
	stored, err := client.GetToolInventory(t.Context(), "revision")
	require.NoError(t, err)
	require.True(t, proto.Equal(catalog, stored))
	catalog.Tools = nil
	require.NoError(t, client.SaveToolInventory(t.Context(), catalog))
	stored, err = client.GetToolInventory(t.Context(), "revision")
	require.NoError(t, err)
	require.NotNil(t, stored, "observed empty differs from unobserved")
	require.Empty(t, stored.Tools)
	catalog.Revision = "missing"
	require.Error(t, client.SaveToolInventory(t.Context(), catalog), "unretained revisions cannot publish inventory")
}
