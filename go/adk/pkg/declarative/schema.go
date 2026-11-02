package declarative

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

// DocumentSchema derives the authoring schema from the same types and field
// rules used by Parse. Semantic graph checks remain in Validate and Build.
func DocumentSchema() (*jsonschema.Schema, error) {
	type nodeShape NodeSpec
	options := &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[NodeSpec]():        {Ref: "#/$defs/Node"},
		reflect.TypeFor[json.RawMessage](): {},
	}}
	node, err := jsonschema.For[nodeShape](options)
	if err != nil {
		return nil, err
	}
	schema, err := jsonschema.For[Spec](options)
	if err != nil {
		return nil, err
	}
	variants := make([]*jsonschema.Schema, 0, len(builtinNodeRules))
	for _, kind := range slices.Sorted(maps.Keys(builtinNodeRules)) {
		rule := builtinNodeRules[kind]
		variant := node.CloneSchemas()
		allowed := append([]string{"id", "type", "description"}, rule.Required...)
		allowed = append(allowed, rule.Optional...)
		for field := range variant.Properties {
			if !slices.Contains(allowed, field) {
				delete(variant.Properties, field)
			}
		}
		variant.Properties["type"].Enum = []any{string(kind)}
		variant.Required = append([]string{"id", "type"}, rule.Required...)
		if kind == NodeLLM {
			variant.Required = slices.DeleteFunc(variant.Required, func(field string) bool { return field == "instruction" })
			variant.AnyOf = []*jsonschema.Schema{{Required: []string{"instruction"}}, {Required: []string{"instruction_from"}}}
		}
		variants = append(variants, variant)
	}
	schema.Defs = map[string]*jsonschema.Schema{"Node": {OneOf: variants}}
	schema.Title = "AgentSystem"
	for _, class := range agentClassNames() {
		schema.Properties["agents"].Items.Properties["agent_class"].Enum = append(schema.Properties["agents"].Items.Properties["agent_class"].Enum, class)
	}
	schema.Properties["apiVersion"].Enum = []any{APIVersion}
	schema.Properties["kind"].Enum = []any{KindAgentSystem}
	schema.Properties["agents"].MinItems = new(1)
	return schema, nil
}
