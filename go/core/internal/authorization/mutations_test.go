package authorization

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	fga "github.com/openfga/go-sdk"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

func TestUncertainMutationUsesAtomicMarkerToFenceReplay(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconfirmed", true: "committed"}[committed], func(t *testing.T) {
			var markerApplied bool
			writes := 0
			binding := fga.TupleKey{User: "principal:alice", Relation: "executor", Object: "agent:workspace-1/agent-1"}
			service, _ := testService(t, func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(request.URL.Path, "/write") {
					writes++
					var wire fga.WriteRequest
					require.NoError(t, json.NewDecoder(request.Body).Decode(&wire))
					tuples := wire.GetWrites().TupleKeys
					require.Len(t, tuples, 2)
					require.Equal(t, "applied", tuples[0].Relation)
					require.Equal(t, binding, tuples[1])
					markerApplied = committed
					writer.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				var wire fga.ReadRequest
				require.NoError(t, json.NewDecoder(request.Body).Decode(&wire))
				query := wire.GetTupleKey()
				response := fga.ReadResponse{}
				if query.GetRelation() == "applied" && markerApplied {
					response.Tuples = []fga.Tuple{{Key: fga.TupleKey{User: query.GetUser(), Relation: query.GetRelation(), Object: query.GetObject()}}}
				}
				require.NoError(t, json.NewEncoder(writer).Encode(response))
			})
			requestID := uuid.NewString()
			err := service.applyMutation(t.Context(), requestID, "principal:admin", binding, true)
			if committed {
				require.NoError(t, err)
				require.NoError(t, service.applyMutation(t.Context(), requestID, "principal:admin", binding, true))
			} else {
				require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable))
			}
			require.Equal(t, 1, writes)
		})
	}
}

func TestMutationPersistsIntentBeforeWritingAuthorization(t *testing.T) {
	writes := 0
	service, store := testService(t, func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/batch-check") {
			writes++
			t.Error("authorization data accessed after failed audit intent")
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"result":{"0":{"allowed":true}}}`))
		require.NoError(t, err)
	})
	store.beginErr = database.ErrConflict
	_, err := service.GrantRole(userContext(t, "admin"), &apiv1.GrantRoleRequest{
		Workspace: workspaceReference().GetWorkspace(), RequestId: uuid.NewString(),
		Binding: &apiv1.RoleBinding{
			Subject: &apiv1.SubjectReference{Subject: &apiv1.SubjectReference_PrincipalId{PrincipalId: "alice"}},
			Role:    apiv1.AccessRole_ACCESS_ROLE_EXECUTOR, Scope: agentReference(),
		},
	})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable))
	require.Zero(t, writes)
}
