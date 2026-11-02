package authorization

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"buf.build/go/protovalidate"
	fga "github.com/openfga/go-sdk"
	fgaclient "github.com/openfga/go-sdk/client"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

type bindingCursor struct {
	Principal string
	Object    string
	Model     string
	Token     string
}

func (s *Service) ListRoleBindings(ctx context.Context, request *apiv1.ListRoleBindingsRequest) (*apiv1.ListRoleBindingsResponse, error) {
	if err := protovalidate.Validate(request); err != nil {
		return nil, serviceerrors.NewInvalidArgument("Invalid role binding query", err)
	}
	principal, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	resource, err := s.resolveBindingScope(ctx, request.Scope, request.Workspace)
	if err != nil {
		return nil, err
	}
	allowed, err := s.check(ctx, principal, resource, apiv1.Permission_PERMISSION_ACCESS_READ)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, serviceerrors.NewPermissionDenied("Role binding access denied", nil)
	}
	cursor := bindingCursor{Principal: principal.ID, Object: resource.object, Model: s.config.AuthorizationModelId}
	token, err := decodeBindingCursor(request.GetPage().GetPageToken(), cursor)
	if err != nil {
		return nil, serviceerrors.NewInvalidArgument("Invalid role binding page token", err)
	}
	limit := request.GetPage().GetLimit()
	if limit == 0 {
		limit = 50
	}
	response, err := s.client.Read(ctx).Body(fgaclient.ClientReadRequest{Object: &resource.object}).
		Options(fgaclient.ClientReadOptions{
			PageSize: &limit, ContinuationToken: &token,
			Consistency: new(fga.CONSISTENCYPREFERENCE_HIGHER_CONSISTENCY),
		}).Execute()
	if err != nil {
		return nil, serviceerrors.NewUnavailable("Authorization bindings unavailable", err)
	}
	result := &apiv1.ListRoleBindingsResponse{Page: new(apiv1.PageResponse)}
	for _, tuple := range response.Tuples {
		role, ok := roleForRelation(resource.kind, tuple.Key.Relation)
		if !ok || tuple.Key.Object != resource.object || tuple.Key.Condition != nil {
			return nil, serviceerrors.NewUnavailable("Unsupported stored authorization binding", nil)
		}
		subject, err := decodeSubject(tuple.Key.User)
		if err != nil {
			return nil, serviceerrors.NewUnavailable("Invalid stored authorization binding", err)
		}
		result.Bindings = append(result.Bindings, &apiv1.RoleBinding{Subject: subject, Role: role, Scope: proto.CloneOf(resource.scope)})
	}
	if response.ContinuationToken != "" {
		cursor.Token = response.ContinuationToken
		data, err := json.Marshal(cursor)
		if err != nil {
			return nil, serviceerrors.NewInternal("Failed to encode binding cursor", err)
		}
		result.Page.NextPageToken = base64.RawURLEncoding.EncodeToString(data)
	}
	return result, nil
}

func decodeBindingCursor(raw string, expected bindingCursor) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 16*1024 {
		return "", fmt.Errorf("binding page token exceeds size limit")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("decode binding page token: %w", err)
	}
	var cursor bindingCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return "", fmt.Errorf("decode binding cursor: %w", err)
	}
	if cursor.Principal != expected.Principal || cursor.Object != expected.Object || cursor.Model != expected.Model || cursor.Token == "" {
		return "", fmt.Errorf("binding page token belongs to a different query")
	}
	return cursor.Token, nil
}
