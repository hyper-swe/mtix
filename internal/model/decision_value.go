// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"
)

// The first invalid field wins. Each reader operates on exact, case-sensitive
// member names from the canonical value and distinguishes absence from null.
type decisionValue struct {
	data any
	path string
	err  *error
}
type decisionObject struct {
	decisionValue
	fields map[string]any
}
type decisionReader[T any] func(decisionValue) T

func (v decisionValue) child(key string, data any) decisionValue {
	return decisionValue{data, v.path + "." + key, v.err}
}

func (v decisionValue) invalid() {
	if *v.err == nil {
		*v.err = decisionInvalid(v.path)
	}
}

func (v decisionValue) unsupported() {
	if *v.err == nil {
		*v.err = fmt.Errorf("%s: %w", v.path, ErrDecisionUnsupported)
	}
}

func decisionField[T any](o decisionObject, key string, required bool, read decisionReader[T]) T {
	var zero T
	if *o.err != nil {
		return zero
	}
	data, ok := o.fields[key]
	v := o.child(key, data)
	if !ok {
		if required {
			v.invalid()
		}
		return zero
	}
	return read(v)
}

func decisionPointer[T any](read decisionReader[T]) decisionReader[*T] {
	return func(v decisionValue) *T { x := read(v); return &x }
}

func decisionNested[T any](read func(decisionObject) T) decisionReader[T] {
	return func(v decisionValue) T {
		m, ok := v.data.(map[string]any)
		if !ok {
			v.invalid()
			var zero T
			return zero
		}
		return read(decisionObject{v, m})
	}
}

func decisionList[T any](limit int, read decisionReader[T]) decisionReader[[]T] {
	return func(v decisionValue) []T {
		a, ok := v.data.([]any)
		if !ok || (limit > 0 && len(a) > limit) {
			v.invalid()
			return nil
		}
		values := make([]T, len(a))
		for i, item := range a {
			values[i] = read(v.child(strconv.Itoa(i), item))
			if *v.err != nil {
				break
			}
		}
		return values
	}
}

func decisionBool(v decisionValue) bool {
	b, ok := v.data.(bool)
	if !ok {
		v.invalid()
	}
	return b
}

func decisionIndex(v decisionValue) int {
	// A v1 request has at most ten links, independently of the machine word size.
	return int(decisionBoundedInteger(v, 9))
}

func decisionSession(v decisionValue) int64 {
	return decisionBoundedInteger(v, 1<<53-1)
}

func decisionBoundedInteger(v decisionValue, maximum float64) int64 {
	n, ok := v.data.(float64)
	if !ok || n < 0 || n > maximum || n != math.Trunc(n) {
		v.invalid()
		return 0
	}
	return int64(n)
}

func decisionSource(v decisionValue) map[string]string {
	m, ok := v.data.(map[string]any)
	if !ok || len(m) > 8 {
		v.invalid()
		return nil
	}
	result := make(map[string]string, len(m))
	for k, x := range m {
		s, ok := x.(string)
		if !ok || !sourceName.MatchString(k) || utf8.RuneCountInString(s) > 120 {
			v.invalid()
			return nil
		}
		result[k] = s
	}
	return result
}

// Decision numbers have already passed through IEEE 754 canonicalization.
// Receipt sequence numbers have no protocol upper bound beyond that representation.
func decisionSequence(v decisionValue) float64 {
	n, ok := v.data.(float64)
	if !ok || n < 0 || math.Trunc(n) != n {
		v.invalid()
		return 0
	}
	return n
}
