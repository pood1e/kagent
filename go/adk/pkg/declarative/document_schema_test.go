package declarative

import (
	"encoding/json"
	"os"
	"testing"
)

func TestDocumentSchemaMatchesNativeTypes(t *testing.T) {
	schema, err := DocumentSchema()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"apiVersion":"adk.declarative/v0.2","kind":"AgentSystem","agents":[{"name":"graph","agent_class":"Workflow","entry":"llm","nodes":[{"id":"llm","type":"llm","model":"stub","instruction_from":{"env":"PROMPT"}}]}]}`,
		`{"apiVersion":"adk.declarative/v0.2","kind":"AgentSystem","agents":[{"name":"graph","agent_class":"Workflow","nodes":[{"id":"sub","type":"workflow","nodes":[{"id":"done","type":"cel","output":"input"}],"edges":[{"from":"START","to":"done"}]}]}]}`,
		`{"apiVersion":"adk.declarative/v0.2","kind":"AgentSystem","agents":[{"name":"loop","agent_class":"LoopAgent","max_iterations":2,"sub_agents":["writer"]},{"name":"writer","agent_class":"LlmAgent","model":"stub","instruction":"Write."}]}`,
	} {
		var document any
		if err := json.Unmarshal([]byte(input), &document); err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(document); err != nil {
			t.Fatalf("native document rejected: %v", err)
		}
	}
	generated, err := os.ReadFile("schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(generated, &want); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(want)
	right, _ := json.Marshal(got)
	if string(left) != string(right) {
		t.Fatal("schema.json differs from native document types; regenerate it")
	}
}
