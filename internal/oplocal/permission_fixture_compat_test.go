// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import "encoding/json"

// Keep existing valid fixture builders byte-for-byte while production propagates
// encoding errors. These adapters are compiled only into the test binary.
func mustPermissionJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
func permissionBatchHash(batch PermissionBatch) string {
	hash, err := checkedPermissionBatchHash(batch)
	if err != nil {
		panic(err)
	}
	return hash
}
func permissionIntentHash(batch PermissionBatch, host string) string {
	hash, err := checkedPermissionIntentHash(batch, host)
	if err != nil {
		panic(err)
	}
	return hash
}
func batchConfirmations(batch PermissionBatch, host string) []Confirmation {
	rows, err := checkedBatchConfirmations(batch, host)
	if err != nil {
		panic(err)
	}
	return rows
}

func permissionAlertInputHash(row Alert) string {
	hash, err := checkedPermissionAlertInputHash(row)
	if err != nil {
		panic(err)
	}
	return hash
}
