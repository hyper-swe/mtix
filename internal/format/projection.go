// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/hyper-swe/mtix/internal/model"
)

// fieldInfo maps a JSON tag name to its struct field index path.
type fieldInfo struct {
	jsonName string
	index    []int
}

// nodeFields is the lazily-initialized whitelist of valid JSON field
// names for model.Node, derived from struct tags at startup. Thread-safe
// via sync.Once.
var (
	nodeFieldsOnce sync.Once
	nodeFields     []fieldInfo
	nodeFieldSet   map[string]fieldInfo
)

// initNodeFields builds the field whitelist from model.Node struct tags.
// Called once via sync.Once.
func initNodeFields() {
	nodeFieldsOnce.Do(func() {
		t := reflect.TypeOf(model.Node{})
		nodeFields = make([]fieldInfo, 0, t.NumField())
		nodeFieldSet = make(map[string]fieldInfo, t.NumField())
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			// Strip ",omitempty" etc.
			name := strings.SplitN(tag, ",", 2)[0]
			if name == "" {
				continue
			}
			fi := fieldInfo{jsonName: name, index: f.Index}
			nodeFields = append(nodeFields, fi)
			nodeFieldSet[name] = fi
		}
	})
}

// ValidFieldNames returns the sorted list of valid projection field
// names for model.Node per FR-17.3. Used for error messages and
// documentation.
func ValidFieldNames() []string {
	initNodeFields()
	names := make([]string, len(nodeFields))
	for i, f := range nodeFields {
		names[i] = f.jsonName
	}
	sort.Strings(names)
	return names
}

// ProjectNode extracts only the requested fields from a node, returning
// a map suitable for JSON marshaling per FR-17.3. Field names are validated
// against the whitelist derived from model.Node JSON tags.
//
// If fields is nil or empty, all fields are returned (no projection).
// Unknown field names return model.ErrInvalidInput with the list of valid names.
// Duplicate field names are silently deduplicated.
//
// Projection happens at the formatter layer in Go, never in SQL SELECT.
// This ensures the store layer always returns complete nodes and the
// projection is a restrictive view (FR-17 audit T5: no information
// expansion).
func ProjectNode(n *model.Node, fields []string) (map[string]any, error) {
	initNodeFields()

	// No projection — return all fields.
	if len(fields) == 0 {
		return nodeToMap(n, nodeFields), nil
	}

	// Validate all field names upfront before extracting any values.
	selected := make([]fieldInfo, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, name := range fields {
		if seen[name] {
			continue
		}
		fi, ok := nodeFieldSet[name]
		if !ok {
			return nil, fmt.Errorf("unknown field %q; valid fields: %s: %w",
				name, strings.Join(ValidFieldNames(), ", "), model.ErrInvalidInput)
		}
		selected = append(selected, fi)
		seen[name] = true
	}

	return nodeToMap(n, selected), nil
}

// ProjectNodes projects a slice of nodes to the given fields per FR-17.3.
// Validates fields once against the first node, then applies to all.
func ProjectNodes(nodes []*model.Node, fields []string) ([]map[string]any, error) {
	initNodeFields()

	if len(nodes) == 0 {
		return []map[string]any{}, nil
	}

	// Validate field names once if projection is requested.
	if len(fields) > 0 {
		if _, err := ProjectNode(nodes[0], fields); err != nil {
			return nil, err
		}
	}

	results := make([]map[string]any, len(nodes))
	for i, n := range nodes {
		m, err := ProjectNode(n, fields)
		if err != nil {
			return nil, err
		}
		results[i] = m
	}
	return results, nil
}

// nodeToMap converts a node to a map using the specified field infos.
// Uses reflection to extract values by struct field index.
func nodeToMap(n *model.Node, fields []fieldInfo) map[string]any {
	v := reflect.ValueOf(n).Elem()
	m := make(map[string]any, len(fields))
	for _, fi := range fields {
		fv := v.FieldByIndex(fi.index)
		m[fi.jsonName] = fv.Interface()
	}
	return m
}
