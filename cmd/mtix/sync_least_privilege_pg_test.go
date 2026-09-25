// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// documentedGrant is one privilege of the documented set for a role that
// syncs without owning the sync tables.
type documentedGrant struct {
	privilege string // SELECT, INSERT, UPDATE or USAGE
	kind      string // SCHEMA, TABLE or SEQUENCE
	object    string // the schema placeholder "", or a table or sequence name
}

// grantTemplates are the GRANT statements the documented set may name, one
// constant template per privilege and object kind; format() on the server
// fills in the identifiers (directive SQL Rule 1a).
var grantTemplates = map[[2]string]string{
	{"USAGE", "SCHEMA"}:    "GRANT USAGE ON SCHEMA public TO %2$I",
	{"SELECT", "TABLE"}:    "GRANT SELECT ON TABLE public.%1$I TO %2$I",
	{"INSERT", "TABLE"}:    "GRANT INSERT ON TABLE public.%1$I TO %2$I",
	{"UPDATE", "TABLE"}:    "GRANT UPDATE ON TABLE public.%1$I TO %2$I",
	{"USAGE", "SEQUENCE"}:  "GRANT USAGE ON SEQUENCE public.%1$I TO %2$I",
	{"SELECT", "SEQUENCE"}: "GRANT SELECT ON SEQUENCE public.%1$I TO %2$I",
}

// documentedSyncGrants reads the privilege list for a role that syncs
// without owning the sync tables from the small-team workflow template,
// the list the customer follows (MTIX-95.1.4). Each bullet is "<PRIVILEGE>
// on <objects>": "the schema", "the sync tables", "the sync tables'
// sequences", or backquoted table and sequence names.
func documentedSyncGrants(t *testing.T) []documentedGrant {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "internal", "docs", "templates", "workflows", "small-team.md.tmpl"))
	require.NoError(t, err)
	doc := string(body)
	start := strings.Index(doc, "A role that syncs without owning")
	require.GreaterOrEqual(t, start, 0, "the small-team workflow lists the privileges")
	end := strings.Index(doc[start:], "```")
	require.Greater(t, end, 0)
	tables, err := migrations.Tables()
	require.NoError(t, err)
	sequences, err := migrations.Sequences()
	require.NoError(t, err)
	isSequence := map[string]bool{}
	for _, s := range sequences {
		isSequence[s] = true
	}
	bullet := regexp.MustCompile(`(?m)^- (SELECT|INSERT|UPDATE|USAGE) on ((?:[^\n]|\n  )+)`)
	ident := regexp.MustCompile("`([a-z_][a-z0-9_]*)`")
	var out []documentedGrant
	for _, m := range bullet.FindAllStringSubmatch(doc[start:start+end], -1) {
		priv, objects := m[1], strings.Join(strings.Fields(m[2]), " ")
		switch {
		case strings.HasPrefix(objects, "the schema"):
			out = append(out, documentedGrant{priv, "SCHEMA", ""})
		case strings.HasPrefix(objects, "the sync tables' sequences"):
			for _, s := range sequences {
				out = append(out, documentedGrant{priv, "SEQUENCE", s})
			}
		case strings.HasPrefix(objects, "the sync tables"):
			for _, tbl := range tables {
				out = append(out, documentedGrant{priv, "TABLE", tbl})
			}
		default:
			for _, n := range ident.FindAllStringSubmatch(objects, -1) {
				kind := "TABLE"
				if isSequence[n[1]] {
					kind = "SEQUENCE"
				}
				out = append(out, documentedGrant{priv, kind, n[1]})
			}
		}
	}
	require.NotEmpty(t, out, "the documented privilege list parses")
	return out
}

// leastPrivilegeEvent builds a valid event for the least-privilege test.
func leastPrivilegeEvent(id, nodeID, author string, op model.OpType, payload string, lamport int64) *model.SyncEvent {
	e := &model.SyncEvent{
		EventID: id, ProjectPrefix: "MTIX", NodeID: nodeID, OpType: op,
		Payload: json.RawMessage(payload), WallClockTS: time.Now().UnixMilli(), LamportClock: lamport,
		VectorClock: model.VectorClock{author: lamport}, AuthorID: author, AuthorMachineHash: "0123456789abcdef",
	}
	if op == model.OpCreateNode {
		e.UID = id // a distinct logical node per create (ADR-003 section 2)
	}
	return e
}

// TestSmallTeamPrivileges_DocumentedSet_SyncsButCannotOpenARestoreWindow:
// a login role holding exactly the privileges the small-team workflow
// documents for a role that syncs without owning the tables can push
// (creates, a same-epoch renumber, a field conflict, a restore collision
// after the owner's mark-restored), pull, and record its client row; the
// same role is refused (permission denied) the mark-restored update of
// sync_hub_state, which only the table owner runs (MTIX-95.1.4).
func TestSmallTeamPrivileges_DocumentedSet_SyncsButCannotOpenARestoreWindow(t *testing.T) {
	grants := documentedSyncGrants(t) // before initTestApp changes the working directory
	initTestApp(t)
	f := newHardenFixture(t)
	owner := f.ownerRole()
	f.migrateAs(owner)
	syncer := f.role("syncer")
	dsn := loginRoleDSN(t, f, syncer)
	for _, g := range grants {
		template, ok := grantTemplates[[2]string{g.privilege, g.kind}]
		require.Truef(t, ok, "no grant form for %s on a %s", g.privilege, g.kind)
		f.ddl(template, g.object, syncer)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ownerPool, err := transport.New(ctx, f.dsnAs(owner), transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	defer ownerPool.Close()
	pool, err := transport.New(ctx, dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	defer pool.Close()

	create := func(id, node, author string, lamport int64) *model.SyncEvent {
		return leastPrivilegeEvent(id, node, author, model.OpCreateNode, `{"title":"x"}`, lamport)
	}
	update := func(id, author, title string) *model.SyncEvent {
		return leastPrivilegeEvent(id, "MTIX-1.4", author, model.OpUpdateField,
			`{"field_name":"title","new_value":"\"`+title+`\""}`, 1)
	}
	push := func(events ...*model.SyncEvent) ([]transport.ConflictDescriptor, []transport.RenumberRequired,
		[]transport.RestoreCollision) {
		t.Helper()
		_, conflicts, renumbers, collisions, err := pool.PushEventsWithCollisions(ctx, events)
		require.NoError(t, err, "the documented set pushes")
		return conflicts, renumbers, collisions
	}
	push(create("0193fa00-0000-7000-8000-0000000b0001", "MTIX-1.4", "alice", 1))
	_, renumbers, _ := push(create("0193fa00-0000-7000-8000-0000000b0002", "MTIX-1.4", "bob", 2))
	require.Len(t, renumbers, 1, "a same-epoch collision renumbers")
	push(update("0193fa00-0000-7000-8000-0000000b0003", "alice", "a"))
	conflicts, _, _ := push(update("0193fa00-0000-7000-8000-0000000b0004", "bob", "b"))
	require.Len(t, conflicts, 1, "the conflict is recorded")

	_, err = ownerPool.MarkRestored(ctx)
	require.NoError(t, err, "the table owner opens the restore window")
	_, _, collisions := push(create("0193fa00-0000-7000-8000-0000000b0005", "MTIX-1.4", "carol", 3))
	require.Len(t, collisions, 1, "the restore collision is recorded")

	events, _, err := pool.PullEvents(ctx, 0, 100)
	require.NoError(t, err, "the documented set pulls")
	require.NotEmpty(t, events)
	require.NoError(t, pool.UpsertProjectClient(ctx, "MTIX", "0123456789abcdef", "0.5.4"))
	require.NoError(t, pool.UpsertProjectClient(ctx, "MTIX", "0123456789abcdef", "0.5.5"), "the client row updates")

	_, err = pool.MarkRestored(ctx)
	require.Error(t, err, "a syncing role cannot open a restore window")
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		require.Equal(t, "42501", pgErr.Code)
	} else {
		require.Contains(t, err.Error(), "permission denied for table sync_hub_state")
	}
}
