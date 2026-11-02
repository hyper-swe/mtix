// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Push states reported to the sender of an addressed send (ADR-007 D21).
const (
	pushStateQueued   = "queued"   // a push was requested and runs after the command returns
	pushStateDeferred = "deferred" // nothing could be started here; the dispatcher or the next push sends it
	pushStateOff      = "off"      // the board is configured not to push on a send
)

// addressedPush is what an addressed send reports about its push request.
type addressedPush struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// addressedPushInputs are the facts the push decision reads.
type addressedPushInputs struct {
	Enabled    bool // sync.push_on_addressed_send
	Mode       service.HubContactMode
	Enrolled   bool // this checkout resolves its own hub DSN or relay
	ListedOnly bool // the host lists the board without enrolling it (ADR-007 A8)
}

// decideAddressedPush says whether an addressed send may request a push and,
// when not, the one reason. Off under manual, off when the switch is false,
// never for a board that has no hub or relay of its own or that the host only
// lists (ADR-007 D21, A8; MTIX-112.20.2).
func decideAddressedPush(in addressedPushInputs) addressedPush {
	switch {
	case !in.Enabled:
		return addressedPush{State: pushStateOff, Reason: service.PushOnAddressedSendKey + " is false"}
	case in.Mode.Kind == service.HubContactManual:
		return addressedPush{State: pushStateOff, Reason: "sync.hub_contact is manual"}
	case in.ListedOnly:
		return addressedPush{State: pushStateOff, Reason: "this board is only listed on this host, not enrolled"}
	case !in.Enrolled:
		return addressedPush{State: pushStateOff, Reason: "no hub or relay is configured for this checkout"}
	}
	return addressedPush{State: pushStateQueued}
}

// addressedPushDeps are the effects of a push request, injected for tests.
type addressedPushDeps struct {
	inputs func() (addressedPushInputs, error)
	// spawn starts the detached child that pushes (the CLI executor).
	spawn func() error
	// inProcess runs the push inside this long-lived process (the MCP
	// executor); when set it is used instead of spawn.
	inProcess func()
}

// requestAddressedPush decides and, when allowed, starts the push. It never
// waits on the network: spawn and inProcess return at once. A refused input
// read or a spawn that cannot start fails closed with a visible state, and the
// event stays pending for the next push (ADR-007 D21; MTIX-112.20.2).
func requestAddressedPush(deps addressedPushDeps) addressedPush {
	in, err := deps.inputs()
	if err != nil {
		return addressedPush{State: pushStateOff, Reason: fmt.Sprintf("could not read push settings: %v", err)}
	}
	res := decideAddressedPush(in)
	if res.State != pushStateQueued {
		return res
	}
	if deps.inProcess != nil {
		deps.inProcess()
		return res
	}
	if err := deps.spawn(); err != nil {
		return addressedPush{State: pushStateDeferred,
			Reason: "could not start a background push here; the host dispatcher or the next mtix sync push sends it"}
	}
	return res
}

// childPushArgv is the detached child's command line. It names no credential:
// the child resolves its hub settings itself, exactly as mtix sync push does.
func childPushArgv() []string { return []string{"sync", "push", "--on-addressed-send"} }

// productionAddressedPushInputs reads the decision facts of the current board.
func productionAddressedPushInputs(ctx context.Context) (addressedPushInputs, error) {
	var in addressedPushInputs
	if app.configSvc == nil || app.mtixDir == "" {
		return in, fmt.Errorf("not in an mtix project")
	}
	var err error
	if in.Enabled, err = app.configSvc.PushOnAddressedSend(); err != nil {
		return in, err
	}
	if in.Mode, _, err = app.configSvc.HubContact(service.GrandfatherInterval); err != nil {
		return in, err
	}
	in.Enrolled = hubConfigured(app.mtixDir) || relayDir() != ""
	if in.ListedOnly, err = listedOnlyBoard(ctx); err != nil {
		return in, err
	}
	return in, nil
}

// listedOnlyBoard reports whether the host registry lists this checkout
// without enrolling it: such a board never pushes from a send (ADR-007 A8).
// A host with no registry has no listing, so the answer is false.
func listedOnlyBoard(ctx context.Context) (bool, error) {
	state, err := operatorState()
	if err != nil {
		return false, err
	}
	registry, err := state.DispatchBoards(ctx)
	if err != nil {
		return false, fmt.Errorf("read host board registry: %w", err)
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(app.mtixDir))
	if err != nil {
		return false, fmt.Errorf("resolve checkout path: %w", err)
	}
	listed := false
	for _, row := range registry.Listed {
		listed = listed || row.Path == root
	}
	for _, row := range registry.Enrolled {
		if row.Path == root {
			return false, nil
		}
	}
	return listed, nil
}

// cliAddressedPush requests the push of a CLI addressed send: a detached
// child under the push lock (ADR-007 D21 executor order, second step).
func cliAddressedPush(ctx context.Context) addressedPush {
	return requestAddressedPush(addressedPushDeps{
		inputs: func() (addressedPushInputs, error) { return productionAddressedPushInputs(ctx) },
		spawn: func() error {
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("find this executable: %w", err)
			}
			spawn := spawnDetached
			if app.detachedSpawn != nil {
				spawn = app.detachedSpawn
			}
			return spawn(exe, filepath.Dir(app.mtixDir), childPushArgv())
		},
	})
}

// pushErrorClass reduces a push error to a short class that is safe to keep:
// the error text itself can name a host, so it is never persisted.
func pushErrorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, transport.ErrDSNNotConfigured):
		return "no_hub_configured"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "push_failed"
}

// recordAddressedPushOutcome persists the child's outcome operator-locally
// (never synced) so the next command and doctor can show a failure.
func recordAddressedPushOutcome(mtixDir string, now func() time.Time, runErr error) error {
	src, err := newHubContactSource(mtixDir)
	if err != nil {
		return err
	}
	rec := oplocal.AddressedPush{At: now(), OK: runErr == nil, ErrorClass: pushErrorClass(runErr)}
	if err := src.state.RecordAddressedPush(src.key(), rec); err != nil {
		return fmt.Errorf("record addressed push outcome: %w", err)
	}
	return nil
}
