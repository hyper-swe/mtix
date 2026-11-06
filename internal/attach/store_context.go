// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import "context"

type storeRootKey struct{}

// WithStoreRoot carries the store root the glue predicate resolved. The attach
// verbs pass it to everything downstream so nothing after the gate discovers a
// store of its own (MTIX-112.19.8).
func WithStoreRoot(ctx context.Context, root string) context.Context {
	return context.WithValue(ctx, storeRootKey{}, root)
}

// StoreRootFrom returns the store root the predicate resolved, and false when
// the context carries none (an adapter then fails closed instead of searching).
func StoreRootFrom(ctx context.Context) (string, bool) {
	root, ok := ctx.Value(storeRootKey{}).(string)
	return root, ok && root != ""
}
