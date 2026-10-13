// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

func validateAlertJSONKeys(data []byte) error {
	if !utf8.Valid(data) {
		return invalid("alert JSON text is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkAlertJSONValue(decoder, 0); err != nil {
		return invalid("alert JSON shape is invalid")
	}
	return nil
}
func walkAlertJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 8 {
		return invalid("alert JSON nesting is invalid")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delimiter == '{' {
		return walkAlertJSONObject(decoder, depth)
	}
	if delimiter != '[' {
		return invalid("alert JSON delimiter is invalid")
	}
	for decoder.More() {
		if validationErr := walkAlertJSONValue(decoder, depth+1); validationErr != nil {
			return validationErr
		}
	}
	_, err = decoder.Token()
	return err
}
func walkAlertJSONObject(decoder *json.Decoder, depth int) error {
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok || seen[name] {
			return invalid("alert JSON keys are invalid")
		}
		seen[name] = true
		if validationErr := walkAlertJSONValue(decoder, depth+1); validationErr != nil {
			return validationErr
		}
	}
	_, err := decoder.Token()
	return err
}
