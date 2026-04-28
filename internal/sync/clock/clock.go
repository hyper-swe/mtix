// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package clock provides the sync subsystem's identifier and identity
// primitives per FR-18: time-prefixed UUID v7 event IDs and a stable
// machine_hash used as the LWW final tie-break (SYNC-DESIGN §8.2).
//
// This package has no dependencies on the rest of mtix — it is
// importable from anywhere in the sync stack without cycles.
package clock

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"sync"

	"github.com/google/uuid"
)

// uuidNewV7 is the seam tests use to inject a CSPRNG failure.
//
//nolint:gochecknoglobals // intentional injection seam
var uuidNewV7 = uuid.NewV7

// osHostname is the seam tests use to inject a hostname-lookup failure.
//
//nolint:gochecknoglobals // intentional injection seam
var osHostname = os.Hostname

// netInterfaces is the seam tests use to inject the no-eligible-interface
// branch and the interface-enumeration error.
//
//nolint:gochecknoglobals // intentional injection seam
var netInterfaces = net.Interfaces

// NewEventID returns a fresh UUID v7. Per RFC 9562, UUID v7 places a
// 48-bit Unix-millisecond timestamp in the high bits, so consecutive
// IDs from the same emitter sort lexically by emission time. The
// remaining 74 bits are random.
//
// Returns an error only on a CSPRNG failure (extremely rare; surfaces
// as a wrapped error rather than a panic).
func NewEventID() (string, error) {
	id, err := uuidNewV7()
	if err != nil {
		return "", fmt.Errorf("uuid v7: %w", err)
	}
	return id.String(), nil
}

// MustNewEventID panics on CSPRNG failure. Use only in tests and code
// paths where a UUID generation failure is unrecoverable.
func MustNewEventID() string {
	id, err := NewEventID()
	if err != nil {
		panic(err)
	}
	return id
}

var (
	machineHashOnce  sync.Once
	machineHashValue string
	machineHashErr   error
)

// MachineHash returns a stable 16-hex-char identifier for the current
// machine. The value is the first 16 hex characters of SHA-256 over a
// canonical envelope of:
//
//   - hostname (os.Hostname; falls back to "unknown" on error)
//   - lowest non-loopback interface MAC (sorted by interface name)
//   - GOOS
//   - GOARCH
//
// The value is computed once per process and cached. It is stable
// across reboots and mtix reinstalls on the same machine; it changes if
// the user renames the host or replaces the network card.
//
// Used as the deterministic final tie-break in LWW conflict resolution
// (SYNC-DESIGN §8.2). The 16-hex prefix is enough to make the lex-min
// comparison stable; full SHA-256 would be wasteful in the wire payload.
func MachineHash() (string, error) {
	machineHashOnce.Do(func() {
		machineHashValue, machineHashErr = computeMachineHash()
	})
	return machineHashValue, machineHashErr
}

// MustMachineHash panics on computation error. Use only when an error
// is unrecoverable (e.g. tests).
func MustMachineHash() string {
	v, err := MachineHash()
	if err != nil {
		panic(err)
	}
	return v
}

func computeMachineHash() (string, error) {
	hostname, err := osHostname()
	if err != nil {
		hostname = "unknown"
	}

	mac, err := lowestNonLoopbackMAC()
	if err != nil {
		return "", fmt.Errorf("read interfaces: %w", err)
	}

	envelope := fmt.Sprintf("hostname=%s|mac=%s|os=%s|arch=%s",
		hostname, mac, runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256([]byte(envelope))
	return hex.EncodeToString(sum[:8]), nil
}

// lowestNonLoopbackMAC returns the MAC address of the lowest-named
// non-loopback interface that has a non-zero hardware address. Returns
// "no-hw-addr" when the machine has no eligible interface (e.g.
// container with only loopback) so the hash remains deterministic
// rather than failing.
func lowestNonLoopbackMAC() (string, error) {
	ifaces, err := netInterfaces()
	if err != nil {
		return "", err
	}
	type entry struct {
		name string
		mac  string
	}
	candidates := make([]entry, 0, len(ifaces))
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(ifc.HardwareAddr) == 0 {
			continue
		}
		candidates = append(candidates, entry{name: ifc.Name, mac: ifc.HardwareAddr.String()})
	}
	if len(candidates) == 0 {
		return "no-hw-addr", nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].name < candidates[j].name
	})
	return candidates[0].mac, nil
}

// resetForTest exposes a knob for the tests in this package to replay
// the once.Do gate. NOT exported outside the package; tests reach in via
// the same-package convention.
//
//nolint:unused // used by test files
func resetForTest() {
	machineHashOnce = sync.Once{}
	machineHashValue = ""
	machineHashErr = nil
}
