// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The hook policy fixture observes the real exec adapter without changing the
// production dispatcher API or the registry's other delivery adapters.
package service

import (
	"fmt"

	"github.com/hyper-swe/mtix/internal/hooks"
)

// WrapExecAdapterForTest installs test-only observation around the dispatcher's
// real exec adapter, preserving every other adapter. It is compiled only in tests.
func WrapExecAdapterForTest(d *HooksDispatcher, wrap func(hooks.Adapter) hooks.Adapter) error {
	exec, ok := d.registry.Lookup(hooks.AdapterExec)
	if !ok {
		return fmt.Errorf("hook policy fixture: exec adapter is absent")
	}
	adapters := []hooks.Adapter{wrap(exec)}
	for _, name := range []string{hooks.AdapterInbox, hooks.AdapterWebhook, hooks.AdapterAppendFile} {
		if adapter, found := d.registry.Lookup(name); found {
			adapters = append(adapters, adapter)
		}
	}
	d.registry = hooks.NewRegistry(adapters...)
	return nil
}
