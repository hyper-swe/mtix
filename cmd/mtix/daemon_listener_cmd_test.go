// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/procinfo"
)

func listenerVerbs(t *testing.T, f *ctlFake, fm *fakeManager) map[string]*cobra.Command {
	t.Helper()
	spec := specFor(t, "linux")
	spec.Path = ""
	cmds := newDaemonServiceCmdsWith(serviceDeps{
		run:      fm.run,
		spec:     func(int) (*serviceSpec, error) { return spec, nil },
		listener: func() (*listenerCtl, error) { return f.ctl(), nil },
	})
	byName := map[string]*cobra.Command{}
	for _, c := range cmds {
		byName[c.Name()] = c
	}
	return byName
}

func runListenerVerb(t *testing.T, c *cobra.Command, args ...string) (string, error) {
	t.Helper()
	require.NoError(t, c.ParseFlags(args))
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetContext(t.Context())
	err := c.RunE(c, nil)
	return out.String(), err
}

func TestDaemonStatusAndStop_ListenerFlag_RunsTheListenerProtocol(t *testing.T) {
	f := newCtlFake()
	fm := &fakeManager{}
	verbs := listenerVerbs(t, f, fm)

	out, err := runListenerVerb(t, verbs["status"], "--listener")
	require.NoError(t, err)
	assert.Contains(t, out, "listener running")
	assert.Empty(t, fm.ran, "--listener never touches the OS service manager")

	f.onSleep = func(f *ctlFake) { f.held = false }
	out, err = runListenerVerb(t, verbs["stop"], "--listener", "--timeout", "10s")
	require.NoError(t, err)
	assert.Contains(t, out, "listener stopped")
	assert.Empty(t, fm.ran)
}

func TestDaemonStatusAndStop_WithoutListenerFlag_KeepServiceBehaviour(t *testing.T) {
	f := newCtlFake()
	fm := &fakeManager{}
	verbs := listenerVerbs(t, f, fm)
	_, err := runListenerVerb(t, verbs["stop"])
	require.NoError(t, err)
	require.NotEmpty(t, fm.ran, "plain stop still runs the service manager")
	assert.Zero(t, f.stopSet, "plain stop never writes a listener stop request")
	_, err = runListenerVerb(t, verbs["status"])
	require.NoError(t, err)
}

func TestDaemonListenerFlags_OnlyOnStatusAndStop(t *testing.T) {
	verbs := listenerVerbs(t, newCtlFake(), &fakeManager{})
	for _, name := range []string{"status", "stop"} {
		assert.NotNil(t, verbs[name].Flags().Lookup("listener"), name)
		assert.NotNil(t, verbs[name].Flags().Lookup("json"), name)
	}
	assert.NotNil(t, verbs["stop"].Flags().Lookup("timeout"))
	for _, name := range []string{"install", "uninstall", "start"} {
		assert.Nil(t, verbs[name].Flags().Lookup("listener"), name)
	}
}

func TestDaemonListenerFlags_JSONOrTimeoutWithoutListener_IsRefused(t *testing.T) {
	verbs := listenerVerbs(t, newCtlFake(), &fakeManager{})
	_, err := runListenerVerb(t, verbs["status"], "--json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--listener")
	_, err = runListenerVerb(t, verbs["stop"], "--timeout", "5s")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--listener")
}

func TestDaemonListenerFlag_TimeoutNotPositive_IsRefused(t *testing.T) {
	verbs := listenerVerbs(t, newCtlFake(), &fakeManager{})
	_, err := runListenerVerb(t, verbs["stop"], "--listener", "--timeout", "0s")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--timeout")
}

func TestDaemonListenerFlag_FactoryError_Surfaces(t *testing.T) {
	boom := errors.New("no project")
	spec := specFor(t, "linux")
	cmds := newDaemonServiceCmdsWith(serviceDeps{run: (&fakeManager{}).run,
		spec:     func(int) (*serviceSpec, error) { return spec, nil },
		listener: func() (*listenerCtl, error) { return nil, boom }})
	for _, c := range cmds {
		if c.Name() == "status" {
			_, err := runListenerVerb(t, c, "--listener")
			require.ErrorIs(t, err, boom)
		}
	}
}

func TestExitCodeForError_ListenerErrors_HaveContractCodes(t *testing.T) {
	assert.Equal(t, exitCodeListenerNone, exitCodeForError(errListenerNotRunning))
	assert.Equal(t, exitCodeListenerUnverified, exitCodeForError(errListenerUnverified))
	assert.Equal(t, exitCodeListenerRespawned, exitCodeForError(errListenerRespawned))
	assert.Equal(t, exitCodeListenerStillRunning, exitCodeForError(errListenerStillRunning))
}

func TestProductionListenerCtl_WiresRealSources(t *testing.T) {
	prev := app.mtixDir
	t.Cleanup(func() { app.mtixDir = prev })
	app.mtixDir = ""
	_, err := productionListenerCtl()
	require.Error(t, err, "outside a project there is nothing to inspect")
	assert.True(t, strings.Contains(err.Error(), "mtix init"))
	_ = time.Second
	_ = procinfo.RoleUnknown
}
