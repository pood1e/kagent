package authorization

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"buf.build/go/protovalidate"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

const workspaceScanLimit = 1000

type workspaceCursor struct {
	Principal string
	Model     string
	Last      string
}

func (s *Service) ListWorkspaces(ctx context.Context, request *apiv1.ListWorkspacesRequest) (*apiv1.ListWorkspacesResponse, error) {
	if err := protovalidate.Validate(request); err != nil {
		return nil, serviceerrors.NewInvalidArgument("Invalid workspace query", err)
	}
	principal, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	cursor := workspaceCursor{Principal: principal.ID, Model: s.config.AuthorizationModelId}
	last, err := decodeWorkspaceCursor(request.GetPage().GetPageToken(), cursor)
	if err != nil {
		return nil, serviceerrors.NewInvalidArgument("Invalid workspace page token", err)
	}
	candidates, err := s.workspaceCandidates(ctx)
	if err != nil {
		return nil, err
	}
	limit := int(request.GetPage().GetLimit())
	if limit == 0 {
		limit = 50
	}
	result := &apiv1.ListWorkspacesResponse{Page: new(apiv1.PageResponse)}
	start := sort.Search(len(candidates), func(i int) bool { return candidates[i].Name > last })
	end := min(start+workspaceScanLimit, len(candidates))
	for offset := start; offset < end; offset += 50 {
		stop := min(offset+50, end)
		checks := make([]relationCheck, 0, stop-offset)
		for _, namespace := range candidates[offset:stop] {
			checks = append(checks, relationCheck{object: "workspace:" + string(namespace.UID), relation: "member"})
		}
		allowed, err := s.batchRelations(ctx, principal, checks)
		if err != nil {
			return nil, err
		}
		for i, namespace := range candidates[offset:stop] {
			cursor.Last = namespace.Name
			if !allowed[i] {
				continue
			}
			current, err := s.resolver.workspace(ctx, namespace.Name, string(namespace.UID))
			if serviceerrors.IsCode(err, serviceerrors.CodeNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			result.Workspaces = append(result.Workspaces, &apiv1.WorkspaceAuthorizationReference{
				Namespace: current.Name, Uid: string(current.UID),
			})
			if len(result.Workspaces) == limit {
				if offset+i+1 < len(candidates) {
					result.Page.NextPageToken, err = encodeWorkspaceCursor(cursor)
					if err != nil {
						return nil, serviceerrors.NewInternal("Failed to encode workspace cursor", err)
					}
				}
				return result, nil
			}
		}
	}
	if end < len(candidates) {
		result.Page.NextPageToken, err = encodeWorkspaceCursor(cursor)
		if err != nil {
			return nil, serviceerrors.NewInternal("Failed to encode workspace cursor", err)
		}
	}
	return result, nil
}

func (s *Service) workspaceCandidates(ctx context.Context) ([]corev1.Namespace, error) {
	var namespaces []corev1.Namespace
	if len(s.watchNamespaces) == 0 {
		list := new(corev1.NamespaceList)
		if err := s.resolver.kube.List(ctx, list); err != nil {
			return nil, serviceerrors.NewUnavailable("Workspace inventory unavailable", err)
		}
		namespaces = list.Items
	} else {
		seen := make(map[string]bool, len(s.watchNamespaces))
		for _, name := range s.watchNamespaces {
			if seen[name] {
				continue
			}
			seen[name] = true
			namespace := new(corev1.Namespace)
			if err := s.resolver.kube.Get(ctx, types.NamespacedName{Name: name}, namespace); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return nil, serviceerrors.NewUnavailable("Workspace inventory unavailable", err)
			}
			namespaces = append(namespaces, *namespace)
		}
	}
	result := make([]corev1.Namespace, 0, len(namespaces))
	for _, namespace := range namespaces {
		if namespace.UID != "" {
			result = append(result, namespace)
		}
	}
	slices.SortFunc(result, func(a, b corev1.Namespace) int { return cmp.Compare(a.Name, b.Name) })
	return result, nil
}

func decodeWorkspaceCursor(raw string, expected workspaceCursor) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 4096 {
		return "", fmt.Errorf("workspace page token exceeds size limit")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("decode workspace page token: %w", err)
	}
	var cursor workspaceCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return "", fmt.Errorf("decode workspace cursor: %w", err)
	}
	if cursor.Principal != expected.Principal || cursor.Model != expected.Model || cursor.Last == "" || len(cursor.Last) > 63 {
		return "", fmt.Errorf("workspace page token belongs to a different query")
	}
	return cursor.Last, nil
}

func encodeWorkspaceCursor(cursor workspaceCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
