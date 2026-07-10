// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import "context"

// Delivery is what an adapter receives when a hook fires: the matched event,
// the hook that matched it, and the full journaled event as JSON. EventJSON is
// passed to exec (stdin/env) and webhook (body) VERBATIM — never interpolated
// into a shell command or a URL (FR-19.3 security).
type Delivery struct {
	Hook      Hook
	Event     Event
	EventJSON []byte
}

// Adapter performs one delivery side effect (FR-19.3). Adapters run
// ASYNCHRONOUSLY after the mutation commits; an adapter error is logged by the
// dispatcher and never propagated to the originating mutation, so a failing or
// slow adapter can never fail or delay the mutation.
type Adapter interface {
	// Name is the adapter's `deliver:` key (AdapterInbox, AdapterExec, ...).
	Name() string
	// Deliver performs the side effect for one matched event.
	Deliver(ctx context.Context, d Delivery) error
}

// Registry maps adapter names to implementations. The dispatcher looks up each
// name in a hook's `deliver:` list; an unknown adapter is skipped with a log
// line rather than failing the whole hook (a partial delivery still beats none).
type Registry struct {
	adapters map[string]Adapter
}

// NewRegistry builds a Registry from the given adapters, keyed by Name().
func NewRegistry(adapters ...Adapter) *Registry {
	r := &Registry{adapters: make(map[string]Adapter, len(adapters))}
	for _, a := range adapters {
		if a != nil {
			r.adapters[a.Name()] = a
		}
	}
	return r
}

// Lookup returns the adapter registered under name, or (nil, false).
func (r *Registry) Lookup(name string) (Adapter, bool) {
	a, ok := r.adapters[name]
	return a, ok
}
