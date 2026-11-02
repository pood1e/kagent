// Package authorization owns native resource access and OpenFGA role bindings.
package authorization

import (
	"fmt"
	"slices"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

type permissionRule struct {
	scopes   []apiv1.AuthorizationScopeKind
	relation string
}

var permissionRules = map[apiv1.Permission]permissionRule{
	apiv1.Permission_PERMISSION_AGENT_READ:       {[]apiv1.AuthorizationScopeKind{agentScope}, "can_agent_read"},
	apiv1.Permission_PERMISSION_AGENT_CREATE:     {nil, "can_agent_create"},
	apiv1.Permission_PERMISSION_AGENT_UPDATE:     {[]apiv1.AuthorizationScopeKind{agentScope}, "can_agent_update"},
	apiv1.Permission_PERMISSION_AGENT_DELETE:     {[]apiv1.AuthorizationScopeKind{agentScope}, "can_agent_delete"},
	apiv1.Permission_PERMISSION_AGENT_INVOKE:     {[]apiv1.AuthorizationScopeKind{agentScope}, "can_agent_invoke"},
	apiv1.Permission_PERMISSION_TEMPLATE_READ:    {[]apiv1.AuthorizationScopeKind{templateScope}, "can_template_read"},
	apiv1.Permission_PERMISSION_TEMPLATE_CREATE:  {nil, "can_template_create"},
	apiv1.Permission_PERMISSION_TEMPLATE_UPDATE:  {[]apiv1.AuthorizationScopeKind{templateScope}, "can_template_update"},
	apiv1.Permission_PERMISSION_TEMPLATE_DELETE:  {[]apiv1.AuthorizationScopeKind{templateScope}, "can_template_delete"},
	apiv1.Permission_PERMISSION_TEMPLATE_USE:     {[]apiv1.AuthorizationScopeKind{templateScope}, "can_template_use"},
	apiv1.Permission_PERMISSION_WORKFLOW_READ:    {[]apiv1.AuthorizationScopeKind{agentScope, templateScope}, "can_workflow_read"},
	apiv1.Permission_PERMISSION_WORKFLOW_EDIT:    {[]apiv1.AuthorizationScopeKind{agentScope, templateScope}, "can_workflow_edit"},
	apiv1.Permission_PERMISSION_WORKFLOW_PUBLISH: {[]apiv1.AuthorizationScopeKind{agentScope, templateScope}, "can_workflow_publish"},
	apiv1.Permission_PERMISSION_WORKFLOW_EXECUTE: {[]apiv1.AuthorizationScopeKind{agentScope, templateScope}, "can_workflow_execute"},
	apiv1.Permission_PERMISSION_SESSION_READ:     {[]apiv1.AuthorizationScopeKind{sessionScope}, "can_session_read"},
	apiv1.Permission_PERMISSION_SESSION_CREATE:   {nil, "can_session_create"},
	apiv1.Permission_PERMISSION_SESSION_SEND:     {[]apiv1.AuthorizationScopeKind{sessionScope}, "can_session_send"},
	apiv1.Permission_PERMISSION_SESSION_SHARE:    {[]apiv1.AuthorizationScopeKind{sessionScope, taskScope}, "can_session_share"},
	apiv1.Permission_PERMISSION_SESSION_DELETE:   {[]apiv1.AuthorizationScopeKind{sessionScope}, "can_session_delete"},
	apiv1.Permission_PERMISSION_TASK_READ:        {[]apiv1.AuthorizationScopeKind{taskScope}, "can_task_read"},
	apiv1.Permission_PERMISSION_TASK_CANCEL:      {[]apiv1.AuthorizationScopeKind{taskScope}, "can_task_cancel"},
	apiv1.Permission_PERMISSION_TASK_RESPOND:     {[]apiv1.AuthorizationScopeKind{taskScope}, "can_task_respond"},
	apiv1.Permission_PERMISSION_ACCESS_READ:      {[]apiv1.AuthorizationScopeKind{agentScope, templateScope, sessionScope, taskScope}, "can_access_read"},
	apiv1.Permission_PERMISSION_ACCESS_MANAGE:    {[]apiv1.AuthorizationScopeKind{agentScope, templateScope, sessionScope, taskScope}, "can_access_manage"},
}

const (
	workspaceScope = apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_WORKSPACE
	agentScope     = apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT
	templateScope  = apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT_TEMPLATE
	sessionScope   = apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_SESSION
	taskScope      = apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_TASK
)

// Role relations are direct assignments; the model owns permission inheritance.
var roleRelations = map[apiv1.AccessRole]map[apiv1.AuthorizationScopeKind]string{
	apiv1.AccessRole_ACCESS_ROLE_WORKSPACE_MEMBER:     {workspaceScope: "member"},
	apiv1.AccessRole_ACCESS_ROLE_DEFINITION_READER:    {workspaceScope: "definition_reader", agentScope: "reader", templateScope: "reader"},
	apiv1.AccessRole_ACCESS_ROLE_DEFINITION_EDITOR:    {workspaceScope: "definition_editor", agentScope: "editor", templateScope: "editor"},
	apiv1.AccessRole_ACCESS_ROLE_WORKFLOW_EDITOR:      {workspaceScope: "workflow_editor", agentScope: "workflow_editor", templateScope: "workflow_editor"},
	apiv1.AccessRole_ACCESS_ROLE_WORKFLOW_PUBLISHER:   {workspaceScope: "workflow_publisher", agentScope: "publisher", templateScope: "publisher"},
	apiv1.AccessRole_ACCESS_ROLE_EXECUTOR:             {workspaceScope: "executor", agentScope: "executor", templateScope: "executor"},
	apiv1.AccessRole_ACCESS_ROLE_EXECUTION_READER:     {workspaceScope: "auditor", sessionScope: "viewer", taskScope: "viewer"},
	apiv1.AccessRole_ACCESS_ROLE_EXECUTION_CONTROLLER: {workspaceScope: "operator", sessionScope: "controller", taskScope: "controller"},
	apiv1.AccessRole_ACCESS_ROLE_ACCESS_ADMIN:         {workspaceScope: "access_admin"},
}

func permissionRelation(kind apiv1.AuthorizationScopeKind, permission apiv1.Permission) (string, error) {
	if kind == workspaceScope {
		switch permission {
		case apiv1.Permission_PERMISSION_SESSION_SEND, apiv1.Permission_PERMISSION_SESSION_SHARE,
			apiv1.Permission_PERMISSION_SESSION_DELETE, apiv1.Permission_PERMISSION_TASK_RESPOND:
			return "", fmt.Errorf("permission %s requires a specific resource", permission)
		}
	}
	rule, ok := permissionRules[permission]
	if !ok || kind != workspaceScope && !slices.Contains(rule.scopes, kind) {
		return "", fmt.Errorf("permission %s is invalid for scope %s", permission, kind)
	}
	return rule.relation, nil
}

func accessRoles() []*apiv1.AccessRoleDefinition {
	result := make([]*apiv1.AccessRoleDefinition, 0, len(roleRelations))
	for role, scopes := range roleRelations {
		definition := &apiv1.AccessRoleDefinition{Role: role}
		for kind := range scopes {
			definition.ScopeKinds = append(definition.ScopeKinds, kind)
		}
		slices.Sort(definition.ScopeKinds)
		result = append(result, definition)
	}
	slices.SortFunc(result, func(a, b *apiv1.AccessRoleDefinition) int { return int(a.Role - b.Role) })
	return result
}

func roleForRelation(kind apiv1.AuthorizationScopeKind, relation string) (apiv1.AccessRole, bool) {
	for role, scopes := range roleRelations {
		if scopes[kind] == relation {
			return role, true
		}
	}
	return apiv1.AccessRole_ACCESS_ROLE_UNSPECIFIED, false
}
