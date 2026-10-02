// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// registryHub is a throwaway hub database whose sync tables are owned by
// owner, the role that runs mtix sync init and mtix sync migrate --yes;
// pool is connected as owner (MTIX-95.44).
type registryHub struct {
	f     *hardenFixture
	owner string
	dsn   string
	pool  *transport.Pool
	ctx   context.Context
}

// newRegistryHub migrates a fresh hub as its owner, with the registry
// index in place, as mtix sync init leaves a hub.
func newRegistryHub(t *testing.T) *registryHub {
	t.Helper()
	f := newHardenFixture(t)
	owner := f.ownerRole()
	f.migrateAs(owner)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	h := &registryHub{f: f, owner: owner, dsn: f.dsnAs(owner), ctx: ctx}
	pool, err := transport.New(ctx, h.dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	h.pool = pool
	return h
}

// create inserts one create row directly, bypassing the push registry, as
// the duplicates a hub may hold from before the registry index existed;
// it returns a unique violation and fails the test on any other error.
func (h *registryHub) create(t *testing.T, eventID, project, number string) error {
	t.Helper()
	_, err := h.f.admin.Exec(h.ctx, `
		INSERT INTO public.sync_events
		  (event_id, project_prefix, node_id, uid, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash)
		VALUES ($1, $2, $3, $1, 'create_node', '{"title":"x"}', 1, 1, '{"alice":1}', 'alice',
		        '0123456789abcdef')`, eventID, project, number)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return err
	}
	require.NoError(t, err)
	return nil
}

// duplicates drops the registry index, as a hub from before the index
// held none, and gives project LEG two creates of LEG-1.
func (h *registryHub) duplicates(t *testing.T) {
	t.Helper()
	h.f.ddlAs(h.owner, "DROP INDEX IF EXISTS public.%I", registryIndex)
	require.NoError(t, h.create(t, "0193fa00-0000-7000-8000-00000095e001", "LEG", "LEG-1"))
	require.NoError(t, h.create(t, "0193fa00-0000-7000-8000-00000095e002", "LEG", "LEG-1"))
}

// openGate registers a remap-aware client for each project, which opens
// the project's version gate.
func (h *registryHub) openGate(t *testing.T, projects ...string) {
	t.Helper()
	for _, p := range projects {
		require.NoError(t, h.pool.UpsertProjectClient(h.ctx, p, "0123456789abcdef", "0.5.5"))
	}
}

// indexState returns "<indisvalid> <indisready>" of the registry index,
// or "absent".
func (h *registryHub) indexState(t *testing.T) string {
	t.Helper()
	got := h.f.strings(`SELECT i.indisvalid::text || ' ' || i.indisready::text
		FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid
		WHERE i.indrelid = 'public.sync_events'::regclass AND c.relname = $1`, registryIndex)
	if len(got) == 0 {
		return "absent"
	}
	return got[0]
}

// setIndexFlags sets indisvalid and indisready of the registry index in
// pg_index, as the superuser, to reproduce each state a failed or
// interrupted build leaves.
func (h *registryHub) setIndexFlags(t *testing.T, valid, ready bool) {
	t.Helper()
	tag, err := h.f.admin.Exec(h.ctx, `UPDATE pg_catalog.pg_index SET indisvalid = $1, indisready = $2
		WHERE indexrelid = to_regclass('public.' || $3)`, valid, ready, registryIndex)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected())
}

// digest is one digest over every sync_events row.
func (h *registryHub) digest(t *testing.T) string {
	t.Helper()
	got := h.f.strings(`SELECT md5(COALESCE(string_agg(e::text, ',' ORDER BY e.event_id), ''))
		FROM public.sync_events e`)
	require.Len(t, got, 1)
	return got[0]
}

// migrate runs `mtix sync migrate --project <project>` as the owner, with
// --yes when yes is set and --json when asJSON is set.
func (h *registryHub) migrate(t *testing.T, project string, yes, asJSON bool) (string, error) {
	t.Helper()
	t.Setenv(transport.EnvDSN, h.dsn)
	app.jsonOutput = asJSON
	var stdout, stderr bytes.Buffer
	err := runSyncMigrate(h.ctx, &stdout, &stderr, nil, transport.Options{InsecureTLS: true}, project, yes)
	return stdout.String(), err
}

// runInit runs `mtix sync init` as the owner and returns its stdout, stderr
// and error.
func (h *registryHub) runInit(t *testing.T) (string, string, error) {
	t.Helper()
	t.Setenv(transport.EnvDSN, h.dsn)
	var stdout, stderr bytes.Buffer
	err := runSyncInit(h.ctx, &stdout, &stderr, nil, transport.Options{InsecureTLS: true})
	return stdout.String(), stderr.String(), err
}

// migrateJSON is the --json report of mtix sync migrate as an agent reads
// it.
type migrateJSON struct {
	Phases []struct {
		Phase   string `json:"phase"`
		Status  string `json:"status"`
		Detail  string `json:"detail"`
		Applied bool   `json:"applied"`
	} `json:"phases"`
	RegistryIndex *struct {
		Present bool `json:"present"`
		Valid   bool `json:"valid"`
		Ready   bool `json:"ready"`
	} `json:"registry_index"`
}

// indexPhase returns the status and detail of the report's 1.5-index
// phase, read from its text or its --json form.
func indexPhase(t *testing.T, out string, asJSON bool) (status, detail string) {
	t.Helper()
	if !asJSON {
		return "", out
	}
	var r migrateJSON
	require.NoError(t, json.Unmarshal([]byte(out), &r), out)
	for _, p := range r.Phases {
		if p.Phase == "1.5-index" {
			return p.Status, p.Detail
		}
	}
	t.Fatalf("no 1.5-index phase in %s", out)
	return "", ""
}

// TestSyncMigrate_ProjectOnHubHoldsDuplicateCreates_EndsWithValidRegistryIndex:
// the developer's scenario (MTIX-95.1.8): the hub has no registry index,
// project LEG holds two creates of LEG-1, and the version gate is open.
// The table owner's mtix sync migrate --yes, for LEG or for another
// project, ends with a valid and ready registry index, changes no event
// row, and the index refuses a third create of LEG-1 (MTIX-95.44).
func TestSyncMigrate_ProjectOnHubHoldsDuplicateCreates_EndsWithValidRegistryIndex(t *testing.T) {
	for _, project := range []string{"LEG", "MTIX"} {
		t.Run(project, func(t *testing.T) {
			initTestApp(t)
			h := newRegistryHub(t)
			h.duplicates(t)
			require.NoError(t, h.create(t, "0193fa00-0000-7000-8000-00000095e003", "MTIX", "MTIX-1"))
			h.openGate(t, "LEG", "MTIX")
			before := h.digest(t)

			out, err := h.migrate(t, project, true, false)
			require.NoError(t, err, "migrate --yes builds the index over the duplicate creates: %s", out)
			require.Contains(t, out, "registry unique index added")
			require.Equal(t, "true true", h.indexState(t), "the registry index is valid and ready")
			require.Equal(t, before, h.digest(t), "no event row is deleted or changed")
			require.Error(t, h.create(t, "0193fa00-0000-7000-8000-00000095e004", "LEG", "LEG-1"),
				"the index refuses another create of the contested number")
		})
	}
}

// TestSyncMigrate_RegistryIndexNotValidOrNotReady_BuildsItAgain: with the
// version gate open, the table owner's mtix sync migrate --yes on a hub
// whose registry index is not valid or not ready drops it and builds it
// again, valid and ready, and never reports it already present, in text
// and in --json (MTIX-95.44).
func TestSyncMigrate_RegistryIndexNotValidOrNotReady_BuildsItAgain(t *testing.T) {
	cases := []struct {
		name         string
		valid, ready bool
		asJSON       bool
	}{
		{"not valid, not ready, text", false, false, false},
		{"not valid, not ready, json", false, false, true},
		{"ready, not valid, json", false, true, true},
		{"valid, not ready, text", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initTestApp(t)
			h := newRegistryHub(t)
			h.openGate(t, "MTIX")
			h.setIndexFlags(t, tc.valid, tc.ready)

			out, err := h.migrate(t, "MTIX", true, tc.asJSON)
			require.NoError(t, err, out)
			status, detail := indexPhase(t, out, tc.asJSON)
			require.NotContains(t, detail, "already present")
			require.Contains(t, detail, "built again")
			require.Equal(t, "true true", h.indexState(t))
			if tc.asJSON {
				require.Equal(t, "ok", status)
				var r migrateJSON
				require.NoError(t, json.Unmarshal([]byte(out), &r))
				require.NotNil(t, r.RegistryIndex, "--json carries the index state")
				require.True(t, r.RegistryIndex.Valid && r.RegistryIndex.Ready)
			}
		})
	}
}

// TestSyncMigrate_RegistryIndexNotValidWithoutBuild_NamesItAndTheFix: when
// migrate does not build (the dry run, or --yes while the version gate is
// closed) and the registry index is not valid, the index phase names the
// state and the fix, never reports the index present, and leaves the
// index as it was (MTIX-95.44).
func TestSyncMigrate_RegistryIndexNotValidWithoutBuild_NamesItAndTheFix(t *testing.T) {
	cases := []struct {
		name        string
		yes, asJSON bool
	}{
		{"dry run, text", false, false},
		{"dry run, json", false, true},
		{"--yes with the gate closed, text", true, false},
		{"--yes with the gate closed, json", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initTestApp(t)
			h := newRegistryHub(t)
			h.setIndexFlags(t, false, false)

			out, err := h.migrate(t, "MTIX", tc.yes, tc.asJSON)
			require.NoError(t, err, out)
			_, detail := indexPhase(t, out, tc.asJSON)
			require.NotContains(t, detail, "already present")
			require.Contains(t, detail, "not valid")
			require.Contains(t, detail, "mtix sync migrate --yes")
			require.Equal(t, "false false", h.indexState(t), "the index is left as it was")
		})
	}
}

// TestSyncInit_RegistryIndexNotValid_ReportsTheFix: migration 009's
// CREATE UNIQUE INDEX IF NOT EXISTS skips an index of the registry's name
// that is not valid, so mtix sync init checks the index after the
// migrations and reports it with the exact fix (MTIX-95.44).
func TestSyncInit_RegistryIndexNotValid_ReportsTheFix(t *testing.T) {
	initTestApp(t)
	h := newRegistryHub(t)
	h.setIndexFlags(t, false, false)

	_, stderr, err := h.runInit(t)
	require.NoError(t, err, stderr)
	require.Contains(t, stderr, registryIndex)
	require.Contains(t, stderr, "not valid")
	require.Contains(t, stderr, "mtix sync migrate --yes")
}

// TestSyncInit_DuplicateCreatesWithoutIndex_RefusesWithTheFixThatWorks: on
// a hub without the registry index whose project holds duplicate creates,
// mtix sync init refuses and names the fix; running that fix, the table
// owner's mtix sync migrate --yes with the gate open, lets init run again
// cleanly over a valid registry index (MTIX-95.44).
func TestSyncInit_DuplicateCreatesWithoutIndex_RefusesWithTheFixThatWorks(t *testing.T) {
	initTestApp(t)
	h := newRegistryHub(t)
	h.duplicates(t)
	h.openGate(t, "LEG")

	_, _, err := h.runInit(t)
	require.Error(t, err, "009 cannot build the index over duplicate creates")
	require.Contains(t, err.Error(), "mtix sync migrate --yes")
	require.Contains(t, err.Error(), "mtix sync init again")

	out, err := h.migrate(t, "LEG", true, false)
	require.NoError(t, err, out)
	_, stderr, err := h.runInit(t)
	require.NoError(t, err, stderr)
	require.NotContains(t, stderr, registryIndex)
	require.Equal(t, "true true", h.indexState(t))
}

// TestSyncDoctor_RegistryIndexNotValidOrNotReady_FailsWithTheFix: the
// schema current check fails, in default mode, when the registry index is
// not valid or not ready, and prints the exact fix with the table owner
// who runs it, in --json; a valid and ready index passes (MTIX-95.44).
func TestSyncDoctor_RegistryIndexNotValidOrNotReady_FailsWithTheFix(t *testing.T) {
	cases := []struct {
		name         string
		valid, ready bool
		fails        bool
	}{
		{"not valid, not ready", false, false, true},
		{"ready, not valid", false, true, true},
		{"valid, not ready", true, false, true},
		{"valid and ready", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initTestApp(t)
			h := newRegistryHub(t)
			h.setIndexFlags(t, tc.valid, tc.ready)
			t.Setenv(transport.EnvDSN, h.dsn)
			app.jsonOutput = true
			var stdout, stderr bytes.Buffer
			err := runSyncDoctor(h.ctx, &stdout, &stderr, nil, transport.Options{InsecureTLS: true})
			var report doctorJSON
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &report), stdout.String())

			pass, warn, detail, fix := doctorCheckNamed(t, report, schemaCurrentName)
			if !tc.fails {
				require.True(t, pass, detail)
				require.NotContains(t, detail, "registry index")
				return
			}
			require.ErrorIs(t, err, errDoctorChecksFailed)
			require.False(t, pass, "the check fails")
			require.False(t, warn)
			require.Contains(t, detail, registryIndex)
			require.Contains(t, fix, "as the table owner ("+h.owner+")")
			require.Contains(t, fix, "mtix sync migrate --yes")
		})
	}
}

// TestSyncMigrate_IndexRefused_ReportsTheRecordedSweep: when mtix sync
// migrate --yes refuses the index build, it still prints, in text and in
// --json, the sweep phase that recorded the duplicates before the refusal
// (MTIX-95.44).
func TestSyncMigrate_IndexRefused_ReportsTheRecordedSweep(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[asJSON], func(t *testing.T) {
			initTestApp(t)
			h := newRegistryHub(t)
			h.f.ddlAs(h.owner, "DROP INDEX IF EXISTS public.%I", registryIndex)
			_, err := h.f.admin.Exec(h.ctx, `INSERT INTO public.sync_events
				  (event_id, project_prefix, node_id, uid, op_type, payload,
				   wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash)
				SELECT gen_random_uuid()::text, 'LEG', 'LEG-' || (g % 101), NULL, 'create_node', '{"title":"x"}',
				       1, g, '{"alice":1}', 'alice', '0123456789abcdef'
				FROM generate_series(1, 202) AS g`)
			require.NoError(t, err)
			h.openGate(t, "LEG")

			out, err := h.migrate(t, "LEG", true, asJSON)
			require.Error(t, err, "101 duplicate creates are above the cap")
			if !asJSON {
				require.Contains(t, out, "renumbered 101 duplicate number(s) in 1 project(s)")
				return
			}
			var r migrateJSON
			require.NoError(t, json.Unmarshal([]byte(out), &r), out)
			require.NotEmpty(t, r.Phases)
			found := false
			for _, p := range r.Phases {
				if p.Phase == "1-sweep" {
					found = p.Applied
				}
			}
			require.True(t, found, "--json reports the applied sweep")
		})
	}
}

// TestSyncDoctor_RegistryIndexMissing_ReportsTheFix: on a hub that holds
// sync_events but no registry index, the schema current check reports the
// missing index with the fix, run as the table owner (MTIX-95.44).
func TestSyncDoctor_RegistryIndexMissing_ReportsTheFix(t *testing.T) {
	initTestApp(t)
	h := newRegistryHub(t)
	h.f.ddlAs(h.owner, "DROP INDEX IF EXISTS public.%I", registryIndex)
	t.Setenv(transport.EnvDSN, h.dsn)
	app.jsonOutput = true
	var stdout, stderr bytes.Buffer
	_ = runSyncDoctor(h.ctx, &stdout, &stderr, nil, transport.Options{InsecureTLS: true})
	var report doctorJSON
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report), stdout.String())

	pass, warn, detail, fix := doctorCheckNamed(t, report, schemaCurrentName)
	require.True(t, pass && warn, "a missing index is a WARN by default: %s", detail)
	require.Contains(t, detail, registryIndex+" is missing")
	require.Contains(t, fix, "as the table owner ("+h.owner+")")
	require.Contains(t, fix, "mtix sync migrate --yes")
}
