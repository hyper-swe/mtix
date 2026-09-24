// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
)

// Catalog markers: which system catalog an object's OID belongs to.
const (
	catRelation byte = 'r' // pg_class: tables and sequences
	catFunction byte = 'f' // pg_proc
)

// publicOID is the grantee OID PostgreSQL uses for PUBLIC in an ACL.
const publicOID uint32 = 0

// firstNormalOID is the first OID of a role an administrator created;
// lower OIDs are the bootstrap superuser and the predefined pg_ roles.
const firstNormalOID uint32 = 16384

// guardFunction is the function every TRUNCATE guard calls.
const guardFunction = "append_only_no_truncate"

// readAllRoles are the predefined roles whose members may read, write or
// maintain every table in the cluster (MTIX-95.1). pg_maintain exists from
// PostgreSQL 17; on earlier servers no role has that name.
var readAllRoles = []string{"pg_read_all_data", "pg_write_all_data", "pg_maintain"}

// objKey identifies a catalog object: its catalog and OID.
type objKey struct {
	cat byte
	oid uint32
}

// hubObject is one sync table, sequence or mtix function.
type hubObject struct {
	key     objKey
	kind    string // FindingKindTable, FindingKindSequence or FindingKindFunction
	schema  string
	name    string
	owner   uint32
	trigger bool // a function that returns trigger: callable only as a trigger
}

// label names the object as findings show it: schema.name, with () after
// a function.
func (o *hubObject) label() string {
	if o.kind == FindingKindFunction {
		return o.schema + "." + o.name + "()"
	}
	return o.schema + "." + o.name
}

// aclEntry is one privilege from an object's ACL (aclexplode), or from a
// column's ACL when column is set.
type aclEntry struct {
	obj       objKey
	grantee   uint32 // publicOID for PUBLIC
	grantor   uint32
	privilege string
	grantable bool
	column    string // "" for a privilege on the whole object
}

// roleInfo is one row of pg_roles.
type roleInfo struct {
	oid        uint32
	name       string
	super      bool
	createRole bool
}

// defaultACL is one privilege of a default-privileges entry.
type defaultACL struct {
	creator   uint32
	schema    string // "" for an entry that applies in all schemas
	objType   string // r (tables), S (sequences) or f (functions)
	grantee   uint32
	privilege string
}

// roleEdge is a direct membership in a read-all predefined role.
type roleEdge struct {
	role      uint32
	member    uint32
	grantor   uint32
	revocable bool // the caller holds ADMIN on role and the grantor's privileges
}

// effPriv is a privilege a role holds on an object, however it got it.
type effPriv struct {
	role      uint32
	obj       objKey
	privilege string
}

// guardState is the state of one TRUNCATE guard.
type guardState struct {
	table    string
	trigger  string
	enabled  string // "" when missing; else pg_trigger.tgenabled
	function string // the function the trigger calls
}

// hubCatalog is everything the privilege verification reads, loaded in
// one transaction (MTIX-95.1).
type hubCatalog struct {
	schema        string
	serverVersion int // server_version_num, such as 160004
	current       uint32
	super         bool // current_user is a superuser
	owners        map[uint32]bool
	roles         map[uint32]roleInfo
	objects       []hubObject
	acl           []aclEntry
	defaults      []defaultACL
	edges         []roleEdge
	usage         map[[2]uint32]bool  // [member, role]: member inherits role's privileges
	member        map[[2]uint32]bool  // [member, role]: any membership: inherit, SET or ADMIN only
	canSet        map[[2]uint32]bool  // [member, role]: member can SET ROLE to role (MEMBER before PG16)
	admin         map[[2]uint32]bool  // [member, role]: member holds ADMIN OPTION on role, directly or through a membership
	superReach    map[uint32][]uint32 // non-superuser role -> superusers it can SET ROLE to
	effective     []effPriv
	guards        []guardState
}

// object returns the object with key k, or nil.
func (c *hubCatalog) object(k objKey) *hubObject {
	for i := range c.objects {
		if c.objects[i].key == k {
			return &c.objects[i]
		}
	}
	return nil
}

// roleName returns the name of the role with oid, or PUBLIC for publicOID.
func (c *hubCatalog) roleName(oid uint32) string {
	if oid == publicOID {
		return "PUBLIC"
	}
	if r, ok := c.roles[oid]; ok {
		return r.name
	}
	return fmt.Sprintf("role %d", oid)
}

// callerScope says whether verification checks the connecting role
// (MTIX-95.1): it does unless the role owns the sync tables, is a superuser
// or is kept.
func (c *hubCatalog) callerScope(kept map[string]bool) string {
	switch {
	case c.owners[c.current]:
		return CallerOwner
	case c.super:
		return CallerSuperuser
	case kept[c.roles[c.current].name]:
		return CallerKept
	default:
		return CallerChecked
	}
}

// ownerNames returns the names of the sync tables' owners, sorted.
func (c *hubCatalog) ownerNames() []string {
	out := make([]string, 0, len(c.owners))
	for oid := range c.owners {
		out = append(out, c.roleName(oid))
	}
	sort.Strings(out)
	return out
}

// loadCatalog reads the sync objects, their privileges, the roles and the
// guard states in tx (MTIX-95.1). It refuses with ErrHardenNotOwner when
// requireOwner is set and the caller does not own every sync table, before
// reading anything else.
func loadCatalog(ctx context.Context, tx pgx.Tx, kept []string, requireOwner bool) (*hubCatalog, error) {
	tables, err := migrations.Tables()
	if err != nil {
		return nil, fmt.Errorf("sync table list: %w", err)
	}
	c := &hubCatalog{owners: map[uint32]bool{}, usage: map[[2]uint32]bool{},
		member: map[[2]uint32]bool{}, canSet: map[[2]uint32]bool{}, admin: map[[2]uint32]bool{},
		superReach: map[uint32][]uint32{}}
	if err := c.loadTables(ctx, tx, tables); err != nil {
		return nil, err
	}
	if err := c.loadCaller(ctx, tx); err != nil {
		return nil, err
	}
	if requireOwner {
		if err := c.checkOwner(ctx, tx); err != nil {
			return nil, err
		}
	}
	if err := c.loadRoles(ctx, tx, kept); err != nil {
		return nil, err
	}
	for _, load := range []func(context.Context, pgx.Tx) error{
		c.loadSequences, c.loadFunctions, c.loadACL, c.loadDefaults, c.loadEdges, c.loadGuards,
	} {
		if err := load(ctx, tx); err != nil {
			return nil, err
		}
	}
	if err := c.loadAccess(ctx, tx, kept); err != nil {
		return nil, err
	}
	return c, nil
}

// loadTables resolves each sync table through the connecting role's
// search_path, as the CLI's unqualified statements do, and records its
// schema and owner. Every table must exist and share one schema.
func (c *hubCatalog) loadTables(ctx context.Context, tx pgx.Tx, tables []string) error {
	// Resolve every migration-defined table name to its pg_class row.
	rows, err := tx.Query(ctx, `
		SELECT t.name, COALESCE(c.oid, 0), COALESCE(n.nspname::text, ''), COALESCE(c.relowner, 0)
		FROM unnest($1::text[]) AS t(name)
		LEFT JOIN pg_catalog.pg_class c ON c.oid = pg_catalog.to_regclass(t.name)
		LEFT JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		ORDER BY t.name`, tables)
	if err != nil {
		return fmt.Errorf("resolve sync tables: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		o := hubObject{kind: FindingKindTable}
		if err := rows.Scan(&o.name, &o.key.oid, &o.schema, &o.owner); err != nil {
			return fmt.Errorf("resolve sync tables: %w", err)
		}
		if o.key.oid == 0 {
			return fmt.Errorf("sync table %s is missing: %w", o.name, ErrSyncSchemaIncomplete)
		}
		if c.schema != "" && c.schema != o.schema {
			return fmt.Errorf("sync tables span schemas %s and %s: %w", c.schema, o.schema, ErrSyncSchemaIncomplete)
		}
		o.key.cat, c.schema = catRelation, o.schema
		c.owners[o.owner] = true
		c.objects = append(c.objects, o)
	}
	return rows.Err()
}

// loadCaller records current_user's OID and superuser status, and the
// server version.
func (c *hubCatalog) loadCaller(ctx context.Context, tx pgx.Tx) error {
	// The calling role, as privilege checks see it, and the server version.
	err := tx.QueryRow(ctx, `
		SELECT r.oid, r.rolsuper, current_setting('server_version_num')::int
		FROM pg_catalog.pg_roles r WHERE r.rolname = current_user`,
	).Scan(&c.current, &c.super, &c.serverVersion)
	if err != nil {
		return fmt.Errorf("read calling role: %w", err)
	}
	return nil
}

// checkOwner refuses unless the caller is a superuser or has the
// privileges of the owner of every sync table (MTIX-95.1). PostgreSQL runs
// a GRANT or REVOKE by such a caller as the owner.
func (c *hubCatalog) checkOwner(ctx context.Context, tx pgx.Tx) error {
	if c.super {
		return nil
	}
	oids := make([]uint32, 0, len(c.objects))
	for _, o := range c.objects {
		oids = append(oids, o.key.oid)
	}
	var owns bool
	// True only when current_user holds the privileges of every table's owner.
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(bool_and(pg_catalog.pg_has_role(current_user, c.relowner, 'USAGE')), false)
		FROM pg_catalog.pg_class c WHERE c.oid = ANY($1::oid[])`, oids).Scan(&owns)
	if err != nil {
		return fmt.Errorf("check table ownership: %w", err)
	}
	if !owns {
		return ErrHardenNotOwner
	}
	return nil
}

// loadRoles reads every role and checks that each kept role exists.
func (c *hubCatalog) loadRoles(ctx context.Context, tx pgx.Tx, kept []string) error {
	// Every role in the cluster; the set is small on any hub.
	rows, err := tx.Query(ctx, `SELECT oid, rolname::text, rolsuper, rolcreaterole FROM pg_catalog.pg_roles`)
	if err != nil {
		return fmt.Errorf("read roles: %w", err)
	}
	defer rows.Close()
	c.roles = map[uint32]roleInfo{}
	byName := map[string]bool{}
	for rows.Next() {
		var r roleInfo
		if err := rows.Scan(&r.oid, &r.name, &r.super, &r.createRole); err != nil {
			return fmt.Errorf("read roles: %w", err)
		}
		c.roles[r.oid] = r
		byName[r.name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read roles: %w", err)
	}
	for _, k := range kept {
		if !byName[k] {
			return fmt.Errorf("kept role %s does not exist: %w", k, model.ErrInvalidInput)
		}
	}
	return nil
}
