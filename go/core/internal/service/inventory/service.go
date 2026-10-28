// Package inventory binds runtime-observed tools to immutable revisions.
package inventory

import (
	"context"
	"errors"
	"slices"
	"strings"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type Store interface {
	GetSessionForRuntime(context.Context, string, string) (*apiv1.Session, error)
	SaveToolInventory(context.Context, *apiv1.BuiltinToolCatalog) error
	GetToolInventory(context.Context, string) (*apiv1.BuiltinToolCatalog, error)
}

type Service struct {
	store      Store
	authorizer auth.Authorizer
}

func NewService(store Store, authorizer auth.Authorizer) *Service {
	return &Service{store: store, authorizer: authorizer}
}

// Report derives revision and Session from verified Actor credentials.
func (s *Service) Report(ctx context.Context, request *runtimev1.ToolInventoryServiceReportRequest) error {
	if err := protovalidate.Validate(request); err != nil {
		return serviceerrors.NewInvalidArgument("Invalid tool inventory", err)
	}
	session, err := runtimeauth.Session(ctx, s.store)
	if err != nil {
		return err
	}
	caller, _ := auth.AuthSessionFrom(ctx)
	if err := s.authorizer.Check(ctx, caller.Principal(), auth.VerbUpdate, auth.Resource{Type: "ToolInventory", Namespace: session.GetAgent().GetNamespace(), Name: session.Id}); err != nil {
		return serviceerrors.NewPermissionDenied("Tool inventory access denied", err)
	}
	tools := proto.Clone(request).(*runtimev1.ToolInventoryServiceReportRequest).Tools
	slices.SortFunc(tools, func(a, b *apiv1.DiscoveredTool) int { return strings.Compare(a.Name, b.Name) })
	if err := s.store.SaveToolInventory(ctx, &apiv1.BuiltinToolCatalog{Revision: session.PreparedRevision, Tools: tools, ObservedAt: timestamppb.Now()}); err != nil {
		return serviceerrors.NewInternal("Failed to record tool inventory", err)
	}
	return nil
}

// Lookup is called after the enclosing Agent read is authorized. Absence
// denotes an unobserved revision, independently of an observed empty catalog.
func (s *Service) Lookup(ctx context.Context, revision string) (*apiv1.BuiltinToolCatalog, error) {
	if revision == "" {
		return nil, nil
	}
	catalog, err := s.store.GetToolInventory(ctx, revision)
	if errors.Is(err, database.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to read tool inventory", err)
	}
	return catalog, nil
}
