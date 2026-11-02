package authorization

import (
	"encoding/json"
	"net/http"
	"testing"

	fga "github.com/openfga/go-sdk"
	"github.com/stretchr/testify/require"

	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

func TestAuthorizationClientRejectsUnsafeConfiguration(t *testing.T) {
	for _, endpoint := range []string{"http://authorization.example", "https://user:secret@authorization.example", "https://authorization.example/path", "file:///secret"} {
		_, _, err := newClient(&configv1.OpenFGAConfig{
			ApiUrl: endpoint, StoreId: testStoreID, AuthorizationModelId: testModelID,
		})
		require.Error(t, err)
	}
	_, _, err := newClient(&configv1.OpenFGAConfig{
		ApiUrl: "https://authorization.example", StoreId: testStoreID, AuthorizationModelId: testModelID,
	})
	require.ErrorContains(t, err, "credential is required")
}

func TestReadyChecksTheActualPinnedModel(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "matching", false: "different"}[valid], func(t *testing.T) {
			service, _ := testService(t, func(writer http.ResponseWriter, request *http.Request) {
				require.Equal(t, "/stores/"+testStoreID+"/authorization-models/"+testModelID, request.URL.Path)
				var model fga.AuthorizationModel
				require.NoError(t, json.Unmarshal(modelJSON, &model))
				if !valid {
					model.TypeDefinitions = model.TypeDefinitions[:len(model.TypeDefinitions)-1]
				}
				model.Id = testModelID
				writer.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(writer).Encode(fga.ReadAuthorizationModelResponse{AuthorizationModel: &model}))
			})
			err := service.Ready(t.Context())
			if valid {
				require.NoError(t, err)
			} else {
				require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeFailedPrecondition))
			}
		})
	}
}
