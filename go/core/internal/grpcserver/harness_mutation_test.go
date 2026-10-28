package grpcserver

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/structuredobject"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

func TestHarnessServiceConditionalMutations(t *testing.T) {
	c := apiv1.NewHarnessServiceClient(newTemplateAndHarnessConnection(t))
	ctx := metadata.AppendToOutgoingContext(t.Context(), "x-user-id", "alice")
	ref := &apiv1.ResourceReference{Namespace: "team", Name: "runtime"}
	candidate := testHarness(ref.Namespace, ref.Name, "pool")
	_, err := c.CreateHarness(ctx, &apiv1.CreateHarnessRequest{Ref: ref, Resource: structured(t, candidate, harnessKind)})
	require.NoError(t, err)
	current, err := c.GetHarness(ctx, &apiv1.GetHarnessRequest{Ref: ref})
	require.NoError(t, err)
	require.NoError(t, structuredobject.ToGo(current.Harness.Resource, harnessKind, candidate, DefaultMaxMessageSize))
	oldVersion := candidate.ResourceVersion
	candidate.Annotations = map[string]string{"display-name": "Production"}
	updated, err := c.UpdateHarness(ctx, &apiv1.UpdateHarnessRequest{Ref: ref, Resource: structured(t, candidate, harnessKind)})
	require.NoError(t, err)
	saved := &v1alpha3.Harness{}
	require.NoError(t, structuredobject.ToGo(updated.Harness.Resource, harnessKind, saved, DefaultMaxMessageSize))
	require.Equal(t, "Production", saved.Annotations["display-name"])
	require.NotEqual(t, oldVersion, saved.ResourceVersion)
	_, err = c.UpdateHarness(ctx, &apiv1.UpdateHarnessRequest{Ref: ref, Resource: structured(t, candidate, harnessKind)})
	assertCode(t, err, codes.Aborted)
	_, err = c.DeleteHarness(ctx, &apiv1.DeleteHarnessRequest{Ref: ref, ResourceVersion: oldVersion})
	assertCode(t, err, codes.Aborted)
	_, err = c.GetHarness(ctx, &apiv1.GetHarnessRequest{Ref: ref})
	require.NoError(t, err)
	_, err = c.DeleteHarness(ctx, &apiv1.DeleteHarnessRequest{Ref: ref, ResourceVersion: saved.ResourceVersion})
	require.NoError(t, err)
}
