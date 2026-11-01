package taskcatalog

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
)

type pageCursor struct {
	Binding string    `json:"scope"`
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
}

func boundaryKeys(agents []*apiv1.ResourceReference) []string {
	unique := map[string]bool{}
	for _, agent := range agents {
		unique[agent.Namespace+"/"+agent.Name] = true
	}
	values := make([]string, 0, len(unique))
	for key := range unique {
		values = append(values, key)
	}
	slices.Sort(values)
	return values
}

func catalogBinding(kind string, q database.CatalogTaskQuery) (string, error) {
	filters := make([]string, 0, len(q.Filters))
	for _, filter := range q.Filters {
		value, err := json.Marshal(filter)
		if err != nil {
			return "", err
		}
		filters = append(filters, string(value))
	}
	slices.Sort(filters)
	value := struct {
		Kind       string
		Scope      database.CatalogScope
		Agent      string
		Filters    []string
		Boundaries []string
		Query      string
	}{Kind: kind, Scope: q.Scope, Filters: filters, Boundaries: q.BoundaryAgents, Query: q.Query}
	if q.Agent != nil {
		value.Agent = q.Agent.Namespace + "/" + q.Agent.Name
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func readCursor(token, binding string) (*time.Time, string, error) {
	if token == "" {
		return nil, "", nil
	}
	if len(token) > 2048 {
		return nil, "", fmt.Errorf("catalog page token exceeds its limit")
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, "", err
	}
	var cursor pageCursor
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return nil, "", err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, "", fmt.Errorf("catalog page token has trailing data")
	}
	if cursor.Binding != binding || cursor.ID == "" || len(cursor.ID) > 256 || cursor.At.IsZero() || cursor.At.Year() > 9999 || cursor.At.Year() < 1 {
		return nil, "", fmt.Errorf("catalog page token scope or position is invalid")
	}
	return &cursor.At, cursor.ID, nil
}

func nextCursor(binding, id string, at *time.Time) (*apiv1.PageResponse, error) {
	result := &apiv1.PageResponse{}
	if id == "" {
		return result, nil
	}
	value := time.Unix(0, 0).UTC()
	if at != nil {
		value = *at
	}
	data, err := json.Marshal(pageCursor{Binding: binding, ID: id, At: value})
	if err != nil {
		return nil, fmt.Errorf("encode catalog page token: %w", err)
	}
	result.NextPageToken = base64.RawURLEncoding.EncodeToString(data)
	return result, nil
}
