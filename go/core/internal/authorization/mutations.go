package authorization

import (
	"context"
	"errors"
	"fmt"

	fga "github.com/openfga/go-sdk"
	fgaclient "github.com/openfga/go-sdk/client"

	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

func (s *Service) applyMutation(ctx context.Context, requestID, actor string, binding fga.TupleKey, grant bool) error {
	marker := fga.TupleKey{User: actor, Relation: "applied", Object: "operation:" + requestID}
	applied, err := s.exactTuple(ctx, marker)
	if err != nil {
		return err
	}
	if applied {
		return nil
	}
	exists, err := s.exactTuple(ctx, binding)
	if err != nil {
		return err
	}
	request := fgaclient.ClientWriteRequest{Writes: []fga.TupleKey{marker}}
	if grant && !exists {
		request.Writes = append(request.Writes, binding)
	} else if !grant && exists {
		request.Deletes = []fga.TupleKeyWithoutCondition{{User: binding.User, Relation: binding.Relation, Object: binding.Object}}
	}
	// The unique marker and binding change share one FGA transaction. Duplicate
	// writes remain errors, fencing replays after a later revocation.
	_, writeErr := s.client.Write(ctx).Body(request).Execute()
	if writeErr == nil {
		return nil
	}
	applied, readErr := s.exactTuple(ctx, marker)
	if readErr == nil && applied {
		return nil
	}
	return serviceerrors.NewUnavailable("Confirm authorization change by retrying the same request_id", errors.Join(writeErr, readErr))
}

func (s *Service) exactTuple(ctx context.Context, tuple fga.TupleKey) (bool, error) {
	response, err := s.client.Read(ctx).Body(fgaclient.ClientReadRequest{
		User: &tuple.User, Relation: &tuple.Relation, Object: &tuple.Object,
	}).Options(fgaclient.ClientReadOptions{
		PageSize: new(int32(1)), Consistency: new(fga.CONSISTENCYPREFERENCE_HIGHER_CONSISTENCY),
	}).Execute()
	if err != nil {
		return false, serviceerrors.NewUnavailable("Authorization data unavailable", err)
	}
	if len(response.Tuples) == 0 {
		return false, nil
	}
	key := response.Tuples[0].Key
	if len(response.Tuples) != 1 || key.User != tuple.User || key.Relation != tuple.Relation || key.Object != tuple.Object || key.Condition != nil {
		return false, serviceerrors.NewUnavailable("Unexpected authorization tuple", fmt.Errorf("tuple identity differs from requested key"))
	}
	return true, nil
}
