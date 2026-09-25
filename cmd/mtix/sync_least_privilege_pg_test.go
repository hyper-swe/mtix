// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// documentedGrant is one privilege of the documented set for a role that
// syncs without owning the sync tables: scope is "" for every syncing
// role, or the command whose runner needs it.
type documentedGrant struct {
	privilege string // SELECT, INSERT, UPDATE, USAGE or EXECUTE
	kind      string // SCHEMA, TABLE, SEQUENCE or FUNCTION
	object    string // "" for the schema, else a table, sequence or function name
	scope     string
}

// The commands the documented list scopes a privilege to (MTIX-95.1.4).
const (
	scopeResolve    = "mtix sync collisions resolve"
	scopeMigrate    = "mtix sync migrate"
	scopeMigrateYes = "mtix sync migrate --yes"
)

// leastPrivilegeMarker starts the documented list in the small-team
// workflow and in docs/SECURITY-MODEL.md.
const leastPrivilegeMarker = "A role that syncs without owning"

// hubSchema is the schema of the least-privilege test hub; PUBLIC holds
// no USAGE on it.
const hubSchema = "hub_data"

// expectedSyncGrants is the exact privilege set both documents must name
// (MTIX-95.1.4): restore collisions are recorded through EXECUTE on the hub
// function record_restore_collision, so the set holds no INSERT on
// sync_node_collisions and no USAGE on its sequence (MTIX-95.1.7).
func expectedSyncGrants() []documentedGrant {
	var out []documentedGrant
	add := func(priv, kind, scope string, objects ...string) {
		for _, o := range objects {
			out = append(out, documentedGrant{priv, kind, o, scope})
		}
	}
	add("USAGE", "SCHEMA", "", "")
	add("SELECT", "TABLE", "", "sync_events", "sync_hub_state", "sync_node_collisions", "sync_project_clients")
	add("INSERT", "TABLE", "", "sync_events", "sync_conflicts", "sync_project_clients")
	add("UPDATE", "TABLE", "", "sync_project_clients")
	add("USAGE", "SEQUENCE", "", "sync_conflicts_conflict_id_seq")
	add("EXECUTE", "FUNCTION", "", "record_restore_collision")
	add("UPDATE", "TABLE", scopeResolve, "sync_node_collisions")
	add("SELECT", "TABLE", scopeMigrate, "node_renumber_remaps")
	add("INSERT", "TABLE", scopeMigrateYes, "node_renumber_remaps")
	return out
}

// readRepoFile returns a file of the repository, by its path from the
// repository root.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, strings.Split(rel, "/")...)...))
	require.NoError(t, err)
	return string(body)
}

// documentedGrantsIn parses the list that follows leastPrivilegeMarker in
// doc: its items, each with any more-indented continuation lines, up to a
// blank line or a less-indented line (MTIX-95.1.4).
func documentedGrantsIn(t *testing.T, doc string) []documentedGrant {
	t.Helper()
	start := strings.Index(doc, leastPrivilegeMarker)
	require.GreaterOrEqual(t, start, 0, "the document states the least-privilege list")
	var items []string
	indent := -1
	for _, line := range strings.Split(doc[start:], "\n")[1:] {
		trimmed := strings.TrimLeft(line, " ")
		lead := len(line) - len(trimmed)
		switch {
		case indent < 0 && !strings.HasPrefix(trimmed, "- "):
			continue // the rest of the paragraph that introduces the list
		case strings.HasPrefix(trimmed, "- ") && (indent < 0 || lead == indent):
			indent = lead
			items = append(items, trimmed)
		case trimmed != "" && lead > indent:
			items[len(items)-1] += " " + trimmed
		default:
			return parseGrantItems(t, items)
		}
	}
	return parseGrantItems(t, items)
}

// grantItem is the form of every item of the documented list.
var grantItem = regexp.MustCompile("^- (SELECT|INSERT|UPDATE|USAGE|EXECUTE) on (.+?)(?:, only for a role that runs `([^`]+)`)?[;.]?$")

// parseGrantItems turns the list items into grants. Every item must read
// "- <SELECT|INSERT|UPDATE|USAGE|EXECUTE> on <objects>", the objects being
// "the schema" or backquoted table, sequence and function names (EXECUTE
// names functions only), optionally ending "only for a role that runs
// `<command>`"; any other item fails the test, so the list names no
// privilege the test does not check (MTIX-95.1.4, MTIX-95.1.7).
func parseGrantItems(t *testing.T, items []string) []documentedGrant {
	t.Helper()
	require.NotEmpty(t, items, "the least-privilege list has items")
	sequences, err := migrations.Sequences()
	require.NoError(t, err)
	functions, err := migrations.Functions()
	require.NoError(t, err)
	ident := regexp.MustCompile("`([a-z_][a-z0-9_]*)`")
	var out []documentedGrant
	for _, item := range items {
		m := grantItem.FindStringSubmatch(item)
		require.NotNilf(t, m, "unrecognised item in the least-privilege list: %q", item)
		if m[2] == "the schema" {
			out = append(out, documentedGrant{m[1], "SCHEMA", "", m[3]})
			continue
		}
		names := ident.FindAllStringSubmatch(m[2], -1)
		require.NotEmptyf(t, names, "unrecognised objects in the least-privilege list: %q", item)
		for _, n := range names {
			kind := "TABLE"
			switch {
			case m[1] == "EXECUTE":
				require.Containsf(t, functions, n[1], "a function the migrations create: %q", item)
				kind = "FUNCTION"
			case strings.HasSuffix(n[1], "_seq"):
				require.Containsf(t, sequences, n[1], "a sequence the migrations create: %q", item)
				kind = "SEQUENCE"
			}
			out = append(out, documentedGrant{m[1], kind, n[1], m[3]})
		}
	}
	return out
}

// grantTemplates are the GRANT statements the documented set may name, one
// constant template per privilege and object kind; format() on the server
// fills in the object, the role and the schema (directive SQL Rule 1a).
var grantTemplates = map[[2]string]string{
	{"USAGE", "SCHEMA"}:   "GRANT USAGE ON SCHEMA %3$I TO %2$I",
	{"SELECT", "TABLE"}:   "GRANT SELECT ON TABLE %3$I.%1$I TO %2$I",
	{"INSERT", "TABLE"}:   "GRANT INSERT ON TABLE %3$I.%1$I TO %2$I",
	{"UPDATE", "TABLE"}:   "GRANT UPDATE ON TABLE %3$I.%1$I TO %2$I",
	{"USAGE", "SEQUENCE"}: "GRANT USAGE ON SEQUENCE %3$I.%1$I TO %2$I",
	// A function name alone is enough while it is not overloaded.
	{"EXECUTE", "FUNCTION"}: "GRANT EXECUTE ON FUNCTION %3$I.%1$I TO %2$I",
}

// leastPrivilegeHub is a hub in hubSchema, owned by owner, and a login
// role, syncer, that holds the documented grants.
type leastPrivilegeHub struct {
	f                   *hardenFixture
	owner, syncer       string
	grants              []documentedGrant
	ownerPool, syncPool *transport.Pool
	syncDSN             string // the syncing role's DSN, hubSchema first on its search_path
	ctx                 context.Context
	lamport             int64
}

// withSearchPath returns dsn with hubSchema as its search_path.
func withSearchPath(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("options", strings.TrimSpace(q.Get("options")+" -c search_path="+hubSchema))
	u.RawQuery = q.Encode()
	return u.String()
}

// newLeastPrivilegeHub migrates the hub as its owner in a schema PUBLIC
// cannot use, and gives a new login role the unscoped documented grants.
func newLeastPrivilegeHub(t *testing.T, grants []documentedGrant) *leastPrivilegeHub {
	t.Helper()
	f := newHardenFixture(t)
	h := &leastPrivilegeHub{f: f, owner: f.ownerRole(), syncer: f.role("syncer"), grants: grants}
	f.ddl("CREATE SCHEMA %I AUTHORIZATION %I", hubSchema, h.owner)
	require.Equal(t, []string{"false"}, f.strings(
		`SELECT pg_catalog.has_schema_privilege('public', $1, 'USAGE')::text`, hubSchema), "PUBLIC holds no USAGE")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	h.ctx = ctx
	var err error
	h.ownerPool, err = transport.New(ctx, withSearchPath(t, f.dsnAs(h.owner)), transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(h.ownerPool.Close)
	require.NoError(t, h.ownerPool.Migrate(ctx))
	h.grant(t, "")
	h.syncDSN = withSearchPath(t, loginRoleDSN(t, f, h.syncer))
	h.syncPool, err = transport.New(ctx, h.syncDSN, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(h.syncPool.Close)
	return h
}

// grant gives the syncing role the documented grants of one scope.
func (h *leastPrivilegeHub) grant(t *testing.T, scope string) {
	t.Helper()
	for _, g := range h.grants {
		if g.scope != scope {
			continue
		}
		template, ok := grantTemplates[[2]string{g.privilege, g.kind}]
		require.Truef(t, ok, "no grant form for %s on a %s", g.privilege, g.kind)
		h.f.ddl(template, g.object, h.syncer, hubSchema)
	}
}

// event builds a valid event with the next Lamport clock; a create gets
// its own uid.
func (h *leastPrivilegeHub) event(node, author string, op model.OpType, payload string) *model.SyncEvent {
	h.lamport++
	id := "0193fa00-0000-7000-8000-00000000" + strconv.FormatInt(1000+h.lamport, 10)
	e := &model.SyncEvent{
		EventID: id, ProjectPrefix: "MTIX", NodeID: node, OpType: op, Payload: json.RawMessage(payload),
		WallClockTS: time.Now().UnixMilli(), LamportClock: h.lamport,
		VectorClock: model.VectorClock{author: h.lamport}, AuthorID: author, AuthorMachineHash: "0123456789abcdef",
	}
	if op == model.OpCreateNode {
		e.UID = id
	}
	return e
}

// push pushes events as the syncing role.
func (h *leastPrivilegeHub) push(t *testing.T, events ...*model.SyncEvent) ([]transport.ConflictDescriptor,
	[]transport.RenumberRequired, []transport.RestoreCollision) {
	t.Helper()
	_, conflicts, renumbers, collisions, err := h.syncPool.PushEventsWithCollisions(h.ctx, events)
	require.NoError(t, err, "the documented set pushes")
	return conflicts, renumbers, collisions
}

// syncs pushes creates, a same-epoch renumber and a field conflict, records
// a restore collision after the owner's mark-restored, pulls, and records
// the client row, all as the syncing role.
func (h *leastPrivilegeHub) syncs(t *testing.T) {
	t.Helper()
	create := func(author string) *model.SyncEvent {
		return h.event("MTIX-1.4", author, model.OpCreateNode, `{"title":"x"}`)
	}
	update := func(author string) *model.SyncEvent {
		e := h.event("MTIX-1.4", author, model.OpUpdateField, `{"field_name":"title","new_value":"\"`+author+`\""}`)
		e.LamportClock, e.VectorClock = 1, model.VectorClock{author: 1}
		return e
	}
	h.push(t, create("alice"))
	_, renumbers, _ := h.push(t, create("bob"))
	require.Len(t, renumbers, 1, "a same-epoch collision renumbers")
	h.push(t, update("alice"))
	conflicts, _, _ := h.push(t, update("bob"))
	require.Len(t, conflicts, 1, "the conflict is recorded")
	_, err := h.ownerPool.MarkRestored(h.ctx)
	require.NoError(t, err, "the table owner runs mark-restored")
	_, _, collisions := h.push(t, create("carol"))
	require.Len(t, collisions, 1, "the restore collision is recorded")
	open, err := h.syncPool.ListOpenCollisions(h.ctx, "MTIX")
	require.NoError(t, err)
	require.Len(t, open, 1, "the hub holds the collision")
	require.Equal(t, [2]int64{0, 1}, [2]int64{open[0].HeldEpoch, open[0].DetectedEpoch},
		"held in epoch 0, detected in epoch 1, as the hub reads them")
	events, _, err := h.syncPool.PullEvents(h.ctx, 0, 100)
	require.NoError(t, err, "the documented set pulls")
	require.NotEmpty(t, events)
	require.NoError(t, h.syncPool.UpsertProjectClient(h.ctx, "MTIX", "0123456789abcdef", "0.5.4"))
	require.NoError(t, h.syncPool.UpsertProjectClient(h.ctx, "MTIX", "0123456789abcdef", "0.5.5"))
}

// requireDenied fails unless err is PostgreSQL's refusal on table.
func requireDenied(t *testing.T, err error, table, what string) {
	t.Helper()
	require.Error(t, err, what)
	require.Contains(t, err.Error(), "permission denied for table "+table, what)
}

// resolves lists and loads the open collision, is refused its resolution
// without the resolve-scoped grant, and resolves it with that grant.
func (h *leastPrivilegeHub) resolves(t *testing.T) {
	t.Helper()
	open, err := h.syncPool.ListOpenCollisions(h.ctx, "MTIX")
	require.NoError(t, err, "the documented set lists collisions")
	require.Len(t, open, 1)
	got, err := h.syncPool.GetOpenCollision(h.ctx, open[0].CollisionID)
	require.NoError(t, err)
	require.Equal(t, open[0].CollisionID, got.CollisionID)
	_, err = h.syncPool.ResolveCollision(h.ctx, got.CollisionID, got.HeldEventID, "MTIX-1.9", "admin")
	requireDenied(t, err, "sync_node_collisions", "resolve needs its scoped grant")
	h.grant(t, scopeResolve)
	done, err := h.syncPool.ResolveCollision(h.ctx, got.CollisionID, got.HeldEventID, "MTIX-1.9", "admin")
	require.NoError(t, err, "the resolve-scoped grant resolves")
	require.True(t, done)
}

// execAsOwner runs a constant statement with bound arguments as the hub's
// owner and commits it.
func (h *leastPrivilegeHub) execAsOwner(t *testing.T, sql string, args ...any) {
	t.Helper()
	tx, err := h.f.admin.Begin(h.ctx)
	require.NoError(t, err)
	if _, err = tx.Exec(h.ctx, `SELECT set_config('role', $1, true)`, h.owner); err == nil {
		_, err = tx.Exec(h.ctx, sql, args...)
	}
	if err != nil {
		require.NoError(t, tx.Rollback(h.ctx))
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit(h.ctx))
}

// migrates gives a project duplicate creates on a hub without the registry
// index, as the owner, then previews and sweeps them as the syncing role,
// each refused without its migrate-scoped grant and allowed with it.
func (h *leastPrivilegeHub) migrates(t *testing.T) {
	t.Helper()
	h.f.ddlAs(h.owner, "DROP INDEX %I.%I", hubSchema, "sync_events_node_registry_uidx")
	for i, id := range []string{"0193fa00-0000-7000-8000-00000000e001", "0193fa00-0000-7000-8000-00000000e002"} {
		h.execAsOwner(t, `INSERT INTO hub_data.sync_events (event_id, project_prefix, node_id, uid, op_type,
			payload, wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash)
			VALUES ($1, 'LEG', 'LEG-1', $1, 'create_node', '{"title":"x"}', 1, $2, '{"alice":1}', 'alice',
			'0123456789abcdef')`, id, i+1)
	}
	_, err := h.syncPool.PreviewDuplicates(h.ctx, "LEG")
	requireDenied(t, err, "node_renumber_remaps", "the migrate preview needs its scoped grant")
	h.grant(t, scopeMigrate)
	n, err := h.syncPool.PreviewDuplicates(h.ctx, "LEG")
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, err = h.syncPool.SweepDuplicates(h.ctx, "LEG")
	requireDenied(t, err, "node_renumber_remaps", "the migrate sweep needs its scoped grant")
	h.grant(t, scopeMigrateYes)
	report, err := h.syncPool.SweepDuplicates(h.ctx, "LEG")
	require.NoError(t, err, "the migrate-scoped grants sweep")
	require.Equal(t, 1, report.Resolved)
}

// TestSmallTeamPrivileges_DocumentedSet_SyncsButCannotRunMarkRestored: the
// small-team workflow and docs/SECURITY-MODEL.md name exactly the expected
// set for a role that syncs without owning the tables. On a hub in a
// schema PUBLIC cannot use, a login role holding that set pushes, pulls,
// records a conflict and, through the hub's recorder, a restore collision
// held in epoch 0 and detected in epoch 1, and lists collisions; the
// resolve and migrate paths need, and work with, their scoped grants; and
// with every grant the role is refused the mark-restored update of
// sync_hub_state, which runs as the table owner (MTIX-95.1.4, MTIX-95.1.7).
func TestSmallTeamPrivileges_DocumentedSet_SyncsButCannotRunMarkRestored(t *testing.T) {
	small := documentedGrantsIn(t, readRepoFile(t, "internal/docs/templates/workflows/small-team.md.tmpl"))
	security := documentedGrantsIn(t, readRepoFile(t, "docs/SECURITY-MODEL.md"))
	require.ElementsMatch(t, expectedSyncGrants(), small, "the small-team workflow names exactly the set")
	require.ElementsMatch(t, small, security, "docs/SECURITY-MODEL.md names the same set")

	initTestApp(t)
	h := newLeastPrivilegeHub(t, small)
	h.syncs(t)
	h.resolves(t)
	h.migrates(t)
	_, err := h.syncPool.MarkRestored(h.ctx)
	requireDenied(t, err, "sync_hub_state", "a syncing role cannot run mark-restored")
}
