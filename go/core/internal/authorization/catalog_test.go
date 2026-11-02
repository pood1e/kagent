package authorization

import (
	"encoding/json"
	"strings"
	"testing"

	fga "github.com/openfga/go-sdk"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func TestModelCoversNativePermissionAndRoleCatalog(t *testing.T) {
	var model fga.AuthorizationModel
	require.NoError(t, json.Unmarshal(modelJSON, &model))
	definitions := make(map[string]fga.TypeDefinition)
	for _, definition := range model.TypeDefinitions {
		definitions[definition.Type] = definition
	}
	types := map[apiv1.AuthorizationScopeKind]string{
		workspaceScope: "workspace", agentScope: "agent", templateScope: "agent_template",
		sessionScope: "session", taskScope: "task",
	}
	for value, name := range apiv1.Permission_name {
		if value == 0 {
			continue
		}
		rule, ok := permissionRules[apiv1.Permission(value)]
		require.True(t, ok, "missing permission %s", name)
		require.Equal(t, "can_"+strings.ToLower(strings.TrimPrefix(name, "PERMISSION_")), rule.relation)
		for kind, objectType := range types {
			relation, err := permissionRelation(kind, apiv1.Permission(value))
			if err == nil {
				require.NotNil(t, definitions[objectType].Relations)
				require.Contains(t, *definitions[objectType].Relations, relation, "%s on %s", name, objectType)
			}
		}
	}
	for value, name := range apiv1.AccessRole_name {
		if value == 0 {
			continue
		}
		scopes, ok := roleRelations[apiv1.AccessRole(value)]
		require.True(t, ok, "missing role %s", name)
		for kind, relation := range scopes {
			_, ok := (*definitions[types[kind]].Relations)[relation]
			require.True(t, ok, "%s on %s", name, types[kind])
			role, ok := roleForRelation(kind, relation)
			require.True(t, ok)
			require.Equal(t, apiv1.AccessRole(value), role)
		}
	}
}
