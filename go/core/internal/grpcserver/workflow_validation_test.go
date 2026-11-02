package grpcserver

import (
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func TestWorkflowRequestValidation(t *testing.T) {
	validator, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	ref := &apiv1.ResourceReference{Namespace: "team", Name: "graph"}
	for _, test := range []struct {
		name    string
		request proto.Message
		valid   bool
	}{
		{"schema", &apiv1.GetWorkflowSchemaRequest{}, true},
		{"persisted revision", &apiv1.GetWorkflowRevisionRequest{Ref: ref}, true},
		{"revision without ref", &apiv1.GetWorkflowRevisionRequest{}, false},
		{"candidate", &apiv1.ValidateWorkflowRequest{Ref: ref, Resource: &apiv1.StructuredObject{}, Document: []byte("candidate")}, true},
		{"missing Agent", &apiv1.ValidateWorkflowRequest{Ref: ref, Document: []byte("candidate")}, false},
		{"empty document", &apiv1.ValidateWorkflowRequest{Ref: ref, Resource: &apiv1.StructuredObject{}}, false},
		{"oversized document", &apiv1.ValidateWorkflowRequest{Ref: ref, Resource: &apiv1.StructuredObject{}, Document: make([]byte, 65537)}, false},
		{"invalid Session", &apiv1.GetSessionWorkflowRequest{SessionId: "invalid"}, false},
		{"Session", &apiv1.GetSessionWorkflowRequest{SessionId: "11111111-1111-4111-8111-111111111111"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validator.Validate(test.request); (err == nil) != test.valid {
				t.Fatalf("Validate() error = %v, valid = %t", err, test.valid)
			}
		})
	}
}
