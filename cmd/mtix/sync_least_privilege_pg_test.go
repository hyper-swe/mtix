// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// unindexedHub is the condition the documented list adds to the command
// of a migrate-scoped privilege: the hub lacks the node-number registry
// index (MTIX-95.1.8).
const unindexedHub = " on a hub without the node-number registry index"

// The scopes the documented list gives a privilege: the command whose
// runner needs it, and for mtix sync migrate the hub it runs on
// (MTIX-95.1.4, MTIX-95.1.8).
const (
	scopeResolve    = "mtix sync collisions resolve"
	scopeMigrate    = "mtix sync migrate" + unindexedHub
	scopeMigrateYes = "mtix sync migrate --yes" + unindexedHub
)

// The documents that state the least-privilege list, by their path from
// the repository root.
const (
	smallTeamPath     = "internal/docs/templates/workflows/small-team.md.tmpl"
	securityModelPath = "docs/SECURITY-MODEL.md"
)

// registryIndex is the node-number registry index migration 009 creates.
const registryIndex = "sync_events_node_registry_uidx"

// migrateIndexSentence is the sentence both documents use for when
// `mtix sync migrate --yes` builds the registry index (MTIX-95.1.8).
const migrateIndexSentence = "On a hub without the node-number registry index, `mtix sync migrate --yes` " +
	"also builds that index when the version gate is open, that is, when every active client of the " +
	"project runs a remap-aware mtix version; only the table owner can build it."

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

// normalizedRepoFile returns a file of the repository with every run of
// white space turned into one space, so a sentence matches however the
// document wraps it (MTIX-95.1.8).
func normalizedRepoFile(t *testing.T, rel string) string {
	t.Helper()
	return strings.Join(strings.Fields(readRepoFile(t, rel)), " ")
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
var grantItem = regexp.MustCompile("^- (SELECT|INSERT|UPDATE|USAGE|EXECUTE) on (.+?)" +
	"(?:, only for a role that runs `([^`]+)`(" + regexp.QuoteMeta(unindexedHub) + ")?)?[;.]?$")

// parseGrantItems turns the list items into grants. Every item must read
// "- <SELECT|INSERT|UPDATE|USAGE|EXECUTE> on <objects>", the objects being
// "the schema" or backquoted table, sequence and function names (EXECUTE
// names functions only), optionally ending "only for a role that runs
// `<command>`", which may add unindexedHub; any other item fails the test,
// so the list names no privilege the test does not check (MTIX-95.1.4,
// MTIX-95.1.7, MTIX-95.1.8).
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
		scope := m[3] + m[4]
		if m[2] == "the schema" {
			out = append(out, documentedGrant{m[1], "SCHEMA", "", scope})
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
			out = append(out, documentedGrant{m[1], kind, n[1], scope})
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
	ownerDSN, syncDSN   string // each role's DSN, hubSchema first on its search_path
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
	h.ownerDSN = withSearchPath(t, f.dsnAs(h.owner))
	h.ownerPool, err = transport.New(ctx, h.ownerDSN, transport.Options{InsecureTLS: true})
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

// migrate runs `mtix sync migrate --project <project>`, with --yes when yes
// is set, connected with dsn, and returns its report and its error
// (MTIX-95.1.8).
func (h *leastPrivilegeHub) migrate(t *testing.T, dsn, project string, yes bool) (string, error) {
	t.Helper()
	t.Setenv(transport.EnvDSN, dsn)
	var stdout, stderr bytes.Buffer
	err := runSyncMigrate(h.ctx, &stdout, &stderr, nil, transport.Options{InsecureTLS: true}, project, yes)
	return stdout.String(), err
}

// remapPrivileges reports whether the syncing role holds SELECT and INSERT
// on node_renumber_remaps, as "<select> <insert>".
func (h *leastPrivilegeHub) remapPrivileges(t *testing.T) string {
	t.Helper()
	got := h.f.strings(`SELECT pg_catalog.has_table_privilege($1, 'hub_data.node_renumber_remaps', 'SELECT')::text
		|| ' ' || pg_catalog.has_table_privilege($1, 'hub_data.node_renumber_remaps', 'INSERT')::text`, h.syncer)
	require.Len(t, got, 1)
	return got[0]
}

// migratesIndexedHub: on the hub as mtix sync init leaves it, with the
// registry index, the syncing role holding only the unscoped grants runs
// `mtix sync migrate` and `--yes` for the synced project, whose version
// gate is open, and --yes finds the index in place (MTIX-95.1.8).
func (h *leastPrivilegeHub) migratesIndexedHub(t *testing.T) {
	t.Helper()
	require.Equal(t, "false false", h.remapPrivileges(t), "the role holds only the unscoped grants")
	out, err := h.migrate(t, h.syncDSN, "MTIX", false)
	require.NoError(t, err, "the unscoped grants preview an indexed hub")
	require.Contains(t, out, "no duplicate numbers")
	out, err = h.migrate(t, h.syncDSN, "MTIX", true)
	require.NoError(t, err, "the unscoped grants run --yes on an indexed hub")
	require.Contains(t, out, "registry index already present")
}

// buildsIndex: on a hub without the registry index and without duplicate
// creates, the syncing role with only the unscoped grants previews the
// synced project, whose version gate is open; its --yes stops at building
// the index, and the table owner's --yes builds it (MTIX-95.1.8).
func (h *leastPrivilegeHub) buildsIndex(t *testing.T) {
	t.Helper()
	h.f.ddlAs(h.owner, "DROP INDEX %I.%I", hubSchema, registryIndex)
	require.Equal(t, "false false", h.remapPrivileges(t), "the role holds only the unscoped grants")
	out, err := h.migrate(t, h.syncDSN, "MTIX", false)
	require.NoError(t, err, "without duplicates the unscoped grants preview an unindexed hub")
	require.Contains(t, out, "no duplicate numbers")
	_, err = h.migrate(t, h.syncDSN, "MTIX", true)
	require.Error(t, err, "only the table owner builds the index")
	require.Contains(t, err.Error(), "must be owner of table sync_events")
	out, err = h.migrate(t, h.ownerDSN, "MTIX", true)
	require.NoError(t, err, "the table owner's --yes builds the index while the version gate is open")
	require.Contains(t, out, "registry unique index added")
	require.Equal(t, []string{registryIndex}, h.f.strings(`SELECT indexname::text FROM pg_catalog.pg_indexes
		WHERE schemaname = $1 AND indexname = $2`, hubSchema, registryIndex))
}

// migrates gives a project duplicate creates on a hub without the registry
// index, as the owner; the syncing role's `mtix sync migrate` and `--yes`
// for that project are each refused without their migrate-scoped grant and
// run with it, and with the project's version gate closed --yes leaves the
// index for later (MTIX-95.1.4, MTIX-95.1.8).
func (h *leastPrivilegeHub) migrates(t *testing.T) {
	t.Helper()
	h.f.ddlAs(h.owner, "DROP INDEX %I.%I", hubSchema, registryIndex)
	for i, id := range []string{"0193fa00-0000-7000-8000-00000000e001", "0193fa00-0000-7000-8000-00000000e002"} {
		h.execAsOwner(t, `INSERT INTO hub_data.sync_events (event_id, project_prefix, node_id, uid, op_type,
			payload, wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash)
			VALUES ($1, 'LEG', 'LEG-1', $1, 'create_node', '{"title":"x"}', 1, $2, '{"alice":1}', 'alice',
			'0123456789abcdef')`, id, i+1)
	}
	_, err := h.migrate(t, h.syncDSN, "LEG", false)
	requireDenied(t, err, "node_renumber_remaps", "the migrate preview needs its scoped grant")
	h.grant(t, scopeMigrate)
	out, err := h.migrate(t, h.syncDSN, "LEG", false)
	require.NoError(t, err, "the migrate-scoped SELECT previews")
	require.Contains(t, out, "1 duplicate number(s) would be renumbered")
	_, err = h.migrate(t, h.syncDSN, "LEG", true)
	requireDenied(t, err, "node_renumber_remaps", "the migrate sweep needs its scoped grant")
	h.grant(t, scopeMigrateYes)
	out, err = h.migrate(t, h.syncDSN, "LEG", true)
	require.NoError(t, err, "the migrate-scoped grants sweep")
	require.Contains(t, out, "renumbered 1 duplicate number(s)")
	require.Contains(t, out, "version gate closed", "with the version gate closed --yes leaves the index for later")
}

// TestSmallTeamPrivileges_DocumentedSet_SyncsButCannotRunMarkRestored: the
// small-team workflow and docs/SECURITY-MODEL.md name exactly the expected
// set for a role that syncs without owning the tables. On a hub in a
// schema PUBLIC cannot use, a login role holding that set pushes, pulls,
// records a conflict and, through the hub's recorder, a restore collision
// held in epoch 0 and detected in epoch 1, and lists collisions; the
// resolve path needs, and works with, its scoped grant; and with every
// grant the role is refused the mark-restored update of sync_hub_state,
// which runs as the table owner (MTIX-95.1.4, MTIX-95.1.7). The migrate
// grants are proved by
// TestSmallTeamPrivileges_MigrateGrants_NeededOnlyOnUnindexedHubWithDuplicates.
func TestSmallTeamPrivileges_DocumentedSet_SyncsButCannotRunMarkRestored(t *testing.T) {
	small := documentedGrantsIn(t, readRepoFile(t, smallTeamPath))
	security := documentedGrantsIn(t, readRepoFile(t, securityModelPath))
	require.ElementsMatch(t, expectedSyncGrants(), small, "the small-team workflow names exactly the set")
	require.ElementsMatch(t, small, security, "docs/SECURITY-MODEL.md names the same set")

	initTestApp(t)
	h := newLeastPrivilegeHub(t, small)
	h.syncs(t)
	h.resolves(t)
	h.grant(t, scopeMigrate)
	h.grant(t, scopeMigrateYes)
	_, err := h.syncPool.MarkRestored(h.ctx)
	requireDenied(t, err, "sync_hub_state", "a syncing role cannot run mark-restored")
}

// TestSmallTeamPrivileges_MigrateGrants_NeededOnlyOnUnindexedHubWithDuplicates:
// both documents scope SELECT and INSERT on node_renumber_remaps to a role
// that runs `mtix sync migrate` on a hub without the node-number registry
// index, and say that --yes builds that index when the version gate is
// open, which only the table owner can do. On a hub in a schema PUBLIC
// cannot use, a login role holding the unscoped grants runs the migrate
// preview and --yes on the hub as init leaves it, with the index, and
// previews a hub without the index and without duplicate creates; the
// table owner's --yes builds the index; and on a hub without the index and
// with duplicate creates the preview and --yes each need, and run with,
// their scoped grant (MTIX-95.1.8).
func TestSmallTeamPrivileges_MigrateGrants_NeededOnlyOnUnindexedHubWithDuplicates(t *testing.T) {
	small := documentedGrantsIn(t, readRepoFile(t, smallTeamPath))
	require.Subset(t, small, []documentedGrant{
		{"SELECT", "TABLE", "node_renumber_remaps", scopeMigrate},
		{"INSERT", "TABLE", "node_renumber_remaps", scopeMigrateYes},
	}, "the migrate grants are scoped to a hub without the registry index")
	for _, rel := range []string{smallTeamPath, securityModelPath} {
		require.Containsf(t, normalizedRepoFile(t, rel), migrateIndexSentence, "%s says when --yes builds the index", rel)
	}

	initTestApp(t)
	h := newLeastPrivilegeHub(t, small)
	h.push(t, h.event("MTIX-1", "alice", model.OpCreateNode, `{"title":"x"}`))
	require.NoError(t, h.syncPool.UpsertProjectClient(h.ctx, "MTIX", "0123456789abcdef", "0.5.5"),
		"a remap-aware client opens the project's version gate")
	h.migratesIndexedHub(t)
	h.buildsIndex(t)
	h.migrates(t)
}

// The backup sentence of the small-team workflow and docs/SECURITY-MODEL.md
// names the sequences between backupSentenceStart and backupSentenceEnd
// (MTIX-95.1.8).
const (
	backupSentenceStart = "A role that runs `mtix sync backup` also needs SELECT on every sync table " +
		"and on the sequences "
	backupSentenceEnd = "; otherwise run the backup as the table owner."
)

// backupNameItem is one sequence of the backup sentence: a backquoted name.
var backupNameItem = regexp.MustCompile("^`([a-z_][a-z0-9_]*)`$")

// backupSequencesIn returns, sorted, the sequences the one backup sentence
// of doc names. The list must read `a`, `b` and `c`: backquoted names
// joined by ", " and a final " and "; any other text in it fails the test
// (MTIX-95.1.8).
func backupSequencesIn(t *testing.T, doc string) []string {
	t.Helper()
	text := strings.Join(strings.Fields(doc), " ")
	require.Equal(t, 1, strings.Count(text, backupSentenceStart), "the document states the backup privileges once")
	_, rest, _ := strings.Cut(text, backupSentenceStart)
	list, _, found := strings.Cut(rest, backupSentenceEnd)
	require.True(t, found, "the backup sentence ends with the table-owner alternative")
	items := strings.Split(list, ", ")
	if head, last, ok := strings.Cut(items[len(items)-1], " and "); ok {
		items = append(items[:len(items)-1], head, last)
	}
	var names []string
	for _, item := range items {
		m := backupNameItem.FindStringSubmatch(item)
		require.NotNilf(t, m, "unrecognised text in the backup sentence's sequence list: %q", item)
		names = append(names, m[1])
	}
	sort.Strings(names)
	return names
}

// TestSmallTeamPrivileges_BackupSentence_NamesEveryHubSequence: the backup
// sentence of the small-team workflow and of docs/SECURITY-MODEL.md names
// every sequence the hub migrations create, as migrations.Sequences()
// lists them, each once, and no other (MTIX-95.1.8).
func TestSmallTeamPrivileges_BackupSentence_NamesEveryHubSequence(t *testing.T) {
	want, err := migrations.Sequences()
	require.NoError(t, err)
	require.NotEmpty(t, want)
	for _, rel := range []string{smallTeamPath, securityModelPath} {
		t.Run(rel, func(t *testing.T) {
			require.Equal(t, want, backupSequencesIn(t, readRepoFile(t, rel)),
				"the backup sentence names every hub sequence once")
		})
	}
}

// The small-team workflow's sentences on CREATE for the role that runs
// `mtix sync init` (MTIX-95.1.8).
const (
	initCreateSentence = "CREATE on the schema is needed whenever `mtix sync init` runs: the first run, " +
		"after an upgrade that adds a migration, and in the restore runbook."
	initCreateBetweenRuns = "Between runs of `mtix sync init` you may drop schema-level CREATE, and grant it " +
		"again before the next run:"
)

// syncInitAs runs `mtix sync init` connected as role and returns its error.
func syncInitAs(t *testing.T, f *hardenFixture, role string) error {
	t.Helper()
	t.Setenv(transport.EnvDSN, f.dsnAs(role))
	var stdout, stderr bytes.Buffer
	return runSyncInit(context.Background(), &stdout, &stderr, nil, transport.Options{InsecureTLS: true})
}

// TestSmallTeamPrivileges_SyncInitOnMigratedHub_NeedsSchemaCreate: the
// small-team workflow says the role that runs `mtix sync init` needs CREATE
// on the schema whenever init runs, and may drop it between runs. On a hub
// that role has migrated, holding USAGE but no CREATE on the schema, init
// fails with PostgreSQL's refusal on the schema; once CREATE is granted
// again, init runs (MTIX-95.1.8).
func TestSmallTeamPrivileges_SyncInitOnMigratedHub_NeedsSchemaCreate(t *testing.T) {
	small := normalizedRepoFile(t, smallTeamPath)
	require.Contains(t, small, initCreateSentence)
	require.Contains(t, small, initCreateBetweenRuns)

	initTestApp(t)
	f := newHardenFixture(t)
	owner := f.ownerRole()
	f.migrateAs(owner)
	f.ddl("REVOKE CREATE ON SCHEMA public FROM %I", owner)
	require.Equal(t, []string{"true false"}, f.strings(`SELECT pg_catalog.has_schema_privilege($1, 'public', 'USAGE')::text
		|| ' ' || pg_catalog.has_schema_privilege($1, 'public', 'CREATE')::text`, owner))
	err := syncInitAs(t, f, owner)
	require.Error(t, err, "init on a migrated hub needs CREATE on the schema")
	require.Contains(t, err.Error(), "permission denied for schema public")

	f.ddl("GRANT CREATE ON SCHEMA public TO %I", owner)
	require.NoError(t, syncInitAs(t, f, owner), "init runs once CREATE is granted again")
}
