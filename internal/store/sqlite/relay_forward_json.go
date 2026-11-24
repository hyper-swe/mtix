// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strings"
)

// samePayloadValue reports whether two JSON payloads hold the same value.
// Numbers compare by exact value (big.Rat), never as float64 and never by
// spelling, so 1e0 equals 1 as in the hub's JSONB while adjacent integers
// beyond 2^53 differ. A number too large to read exactly is not equal.
func samePayloadValue(a, b string) (bool, error) {
	va, err := decodeExactJSON(a)
	if err != nil {
		return false, fmt.Errorf("decode pending relay payload: %w", err)
	}
	vb, err := decodeExactJSON(b)
	if err != nil {
		return false, fmt.Errorf("decode pulled relay payload: %w", err)
	}
	return jsonValuesEqual(va, vb), nil
}

func decodeExactJSON(raw string) (any, error) {
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("invalid payload JSON: %w", err)
	}
	return v, nil
}

func jsonValuesEqual(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		return ok && numbersEqual(x, y)
	case []any:
		y, ok := b.([]any)
		return ok && jsonSlicesEqual(x, y)
	case map[string]any:
		y, ok := b.(map[string]any)
		return ok && jsonMapsEqual(x, y)
	default:
		return reflect.DeepEqual(a, b)
	}
}

func jsonSlicesEqual(x, y []any) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if !jsonValuesEqual(x[i], y[i]) {
			return false
		}
	}
	return true
}

func jsonMapsEqual(x, y map[string]any) bool {
	if len(x) != len(y) {
		return false
	}
	for k, v := range x {
		if w, present := y[k]; !present || !jsonValuesEqual(v, w) {
			return false
		}
	}
	return true
}

func numbersEqual(a, b json.Number) bool {
	ra, ok := new(big.Rat).SetString(a.String())
	if !ok {
		return false
	}
	rb, ok := new(big.Rat).SetString(b.String())
	return ok && ra.Cmp(rb) == 0
}
