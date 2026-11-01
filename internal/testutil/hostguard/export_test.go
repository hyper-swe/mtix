// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hostguard

// RemoveTreeForTest exposes removeTree to the external test package.
func RemoveTreeForTest(root string) error { return removeTree(root) }

// SharedTempForTest exposes sharedTemp to the external test package.
func SharedTempForTest(dir string) bool { return sharedTemp(dir) }

// ChooseBaseForTest exposes chooseBase to the external test package.
func ChooseBaseForTest(env map[string]string, tempDir, realHome string) (string, error) {
	return chooseBase(func(k string) string { return env[k] }, tempDir, realHome)
}
