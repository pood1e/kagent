package mcp

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"

	"buf.build/go/protovalidate"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

//go:embed schema/*.json
var toolSchemas embed.FS

var toolJSON = protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true}

func addProtoTool[In, Out proto.Message](server *mcp.Server, tool *mcp.Tool, newInput func() In, newOutput func() Out,
	handler func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error),
) error {
	input, err := messageSchema(newInput())
	if err != nil {
		return err
	}
	output, err := messageSchema(newOutput())
	if err != nil {
		return err
	}
	tool.InputSchema, tool.OutputSchema = input, output
	// ProtoJSON and Protovalidate own decoding and defaults. The SDK's typed
	// adapter applies JSON Schema defaults before decoding, which differs from
	// protobuf presence and required-value semantics.
	server.AddTool(tool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		input := newInput()
		if err := protojson.Unmarshal(request.Params.Arguments, input); err != nil {
			return toolError(err), nil
		}
		if err := protovalidate.Validate(input); err != nil {
			return toolError(err), nil
		}
		result, output, err := handler(ctx, request, input)
		if err != nil {
			return result, err
		}
		encoded, err := toolJSON.Marshal(output)
		if err != nil {
			return nil, err
		}
		if result == nil {
			result = &mcp.CallToolResult{}
		}
		result.StructuredContent = json.RawMessage(encoded)
		if len(result.Content) == 0 {
			result.Content = textContent("%s", encoded)
		}
		return result, nil
	})
	return nil
}

func messageSchema(message proto.Message) (*jsonschema.Schema, error) {
	name := string(message.ProtoReflect().Descriptor().FullName())
	data, err := toolSchemas.ReadFile("schema/" + name + ".schema.bundle.json")
	if err != nil {
		return nil, fmt.Errorf("load %s schema: %w", name, err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("decode %s schema: %w", name, err)
	}
	schema.ID = ""
	schema.Type = "object"
	return &schema, nil
}
