// Command workflow-schema emits the native declarative authoring schema.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/kagent-dev/kagent/go/adk/pkg/declarative"
)

func main() {
	schema, err := declarative.DocumentSchema()
	if err == nil {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(schema)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
