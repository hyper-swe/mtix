// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package clock

import (
	"errors"
	"net"
	"regexp"
	"sort"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var uuidV7Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewEventID_FormatV7(t *testing.T) {
	for i := 0; i < 50; i++ {
		id, err := NewEventID()
		require.NoError(t, err)
		require.Truef(t, uuidV7Pattern.MatchString(id),
			"id %q does not match UUID v7 grammar", id)
	}
}

func TestNewEventID_TimestampOrderedWithinSameMillisecond(t *testing.T) {
	const n = 200
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = MustNewEventID()
	}
	sorted := make([]string, n)
	copy(sorted, ids)
	sort.Strings(sorted)
	require.Equal(t, sorted, ids,
		"UUID v7 ids generated in sequence should already be lex-sorted "+
			"(timestamp prefix dominates 48 bits)")
}

func TestNewEventID_Unique(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := MustNewEventID()
		_, dup := seen[id]
		require.Falsef(t, dup, "duplicate UUID v7 generated: %s", id)
		seen[id] = struct{}{}
	}
}

func TestMachineHash_FormatAndStability(t *testing.T) {
	resetForTest()
	first, err := MachineHash()
	require.NoError(t, err)
	require.Regexp(t, `^[a-f0-9]{16}$`, first,
		"MachineHash MUST be 16 lowercase hex characters")

	second, err := MachineHash()
	require.NoError(t, err)
	require.Equal(t, first, second, "stable across calls within a process")

	// Reset to prove cache is reseedable but the COMPUTED value remains
	// the same on the same machine.
	resetForTest()
	third, err := MachineHash()
	require.NoError(t, err)
	require.Equal(t, first, third, "stable across cache resets on the same machine")
}

func TestMachineHash_CachedConcurrentAccess(t *testing.T) {
	resetForTest()
	const goroutines = 20
	results := make([]string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			h, err := MachineHash()
			require.NoError(t, err)
			results[idx] = h
		}(i)
	}
	wg.Wait()
	for i := 1; i < goroutines; i++ {
		require.Equal(t, results[0], results[i],
			"all concurrent callers see the same hash")
	}
}

func TestMustMachineHash_DoesNotPanicOnHappyPath(t *testing.T) {
	resetForTest()
	require.NotPanics(t, func() {
		_ = MustMachineHash()
	})
}

func TestMustNewEventID_DoesNotPanicOnHappyPath(t *testing.T) {
	require.NotPanics(t, func() {
		_ = MustNewEventID()
	})
}

func TestNewEventID_PropagatesCSPRNGFailure(t *testing.T) {
	prev := uuidNewV7
	t.Cleanup(func() { uuidNewV7 = prev })
	uuidNewV7 = func() (uuid.UUID, error) {
		return uuid.UUID{}, errors.New("synthetic csprng failure")
	}
	_, err := NewEventID()
	require.Error(t, err)
	require.Contains(t, err.Error(), "uuid v7")
}

func TestMustNewEventID_PanicsOnCSPRNGFailure(t *testing.T) {
	prev := uuidNewV7
	t.Cleanup(func() { uuidNewV7 = prev })
	uuidNewV7 = func() (uuid.UUID, error) {
		return uuid.UUID{}, errors.New("synthetic csprng failure")
	}
	require.Panics(t, func() { _ = MustNewEventID() })
}

func TestComputeMachineHash_HostnameFailureFallsBackToUnknown(t *testing.T) {
	prevHost := osHostname
	t.Cleanup(func() {
		osHostname = prevHost
		resetForTest()
	})
	osHostname = func() (string, error) {
		return "", errors.New("synthetic hostname failure")
	}
	resetForTest()
	h, err := MachineHash()
	require.NoError(t, err, "hostname error must not abort the hash; falls back to 'unknown'")
	require.Regexp(t, `^[a-f0-9]{16}$`, h)
}

func TestComputeMachineHash_NoEligibleInterfacesProducesDeterministicHash(t *testing.T) {
	prevIf := netInterfaces
	t.Cleanup(func() {
		netInterfaces = prevIf
		resetForTest()
	})
	netInterfaces = func() ([]net.Interface, error) {
		return []net.Interface{{Name: "lo", Flags: net.FlagLoopback}}, nil
	}
	resetForTest()
	h, err := MachineHash()
	require.NoError(t, err, "no-hw-addr machine MUST still produce a hash, not an error")
	require.Regexp(t, `^[a-f0-9]{16}$`, h)
}

func TestComputeMachineHash_InterfaceEnumerationErrorPropagates(t *testing.T) {
	prevIf := netInterfaces
	t.Cleanup(func() {
		netInterfaces = prevIf
		resetForTest()
	})
	netInterfaces = func() ([]net.Interface, error) {
		return nil, errors.New("synthetic interface enumeration failure")
	}
	resetForTest()
	_, err := MachineHash()
	require.Error(t, err)
	require.Contains(t, err.Error(), "read interfaces")
}

func TestMustMachineHash_PanicsOnError(t *testing.T) {
	prevIf := netInterfaces
	t.Cleanup(func() {
		netInterfaces = prevIf
		resetForTest()
	})
	netInterfaces = func() ([]net.Interface, error) {
		return nil, errors.New("synthetic interface failure")
	}
	resetForTest()
	require.Panics(t, func() { _ = MustMachineHash() })
}
