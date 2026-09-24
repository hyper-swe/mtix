// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Role OIDs of the synthetic catalog. Normal roles start at 16384; the
// predefined read-all role keeps its real OID below that.
const (
	tOwner   uint32 = 20001
	tTeam    uint32 = 20002
	tAnon    uint32 = 20003
	tUnrel   uint32 = 20004
	tReader  uint32 = 20005
	tAlice   uint32 = 20006 // member of the kept team role
	tGroup   uint32 = 20007 // non-kept group holding a grant
	tBob     uint32 = 20008 // member of that group
	tCarol   uint32 = 20009 // member of the owner role
	tBadName uint32 = 20010 // name outside the identifier form
	tCaller  uint32 = 20011 // current_user, a member of the owner role
	tSuper   uint32 = 10
	tReadAll uint32 = 6181

	tTable uint32 = 30001
	tSeq   uint32 = 30002
	tFunc  uint32 = 30003
)

var (
	keyTable = objKey{cat: catRelation, oid: tTable}
	keySeq   = objKey{cat: catRelation, oid: tSeq}
	keyFunc  = objKey{cat: catFunction, oid: tFunc}
)

// baseCatalog is a clean hub: the owner's own grants, one kept team role
// with plain grants, and intact guards.
func baseCatalog() *hubCatalog {
	return &hubCatalog{
		schema:  "public",
		current: tCaller,
		owners:  map[uint32]bool{tOwner: true},
		roles: map[uint32]roleInfo{
			tOwner: {oid: tOwner, name: "owner"}, tTeam: {oid: tTeam, name: "team"},
			tAnon: {oid: tAnon, name: "anon"}, tUnrel: {oid: tUnrel, name: "unrel"},
			tReader: {oid: tReader, name: "reader"}, tAlice: {oid: tAlice, name: "alice"},
			tGroup: {oid: tGroup, name: "grp"}, tBob: {oid: tBob, name: "bob"},
			tCarol: {oid: tCarol, name: "carol"}, tBadName: {oid: tBadName, name: "Mixed-Case"},
			tCaller: {oid: tCaller, name: "caller"}, tSuper: {oid: tSuper, name: "postgres", super: true},
			tReadAll: {oid: tReadAll, name: "pg_read_all_data"},
		},
		objects: []hubObject{
			{key: keyTable, kind: FindingKindTable, schema: "public", name: "audit_log", owner: tOwner},
			{key: keySeq, kind: FindingKindSequence, schema: "public", name: "audit_log_audit_id_seq", owner: tOwner},
			{key: keyFunc, kind: FindingKindFunction, schema: "public", name: "audit_log_immutable", owner: tOwner},
		},
		acl: []aclEntry{
			{obj: keyTable, grantee: tOwner, privilege: "SELECT"},
			{obj: keyTable, grantee: tTeam, privilege: "SELECT"},
			{obj: keyTable, grantee: tTeam, privilege: "INSERT"},
			{obj: keySeq, grantee: tTeam, privilege: "USAGE"},
		},
		usage: map[[2]uint32]bool{},
		guards: []guardState{
			{table: "audit_log", trigger: "audit_log_no_truncate", enabled: "O", function: guardFunction},
		},
	}
}

// keptTeam is the kept-role set used by most cases.
func keptTeam() map[string]bool { return map[string]bool{"team": true} }

// summary renders a finding as role|kind|object|via|privileges|fix-shape.
func summary(f finding) string {
	fix := "-"
	if f.fix != nil {
		fix = f.fix.shape()
	}
	manual := ""
	if f.manual != nil {
		manual = "|manual:" + f.manual.shape()
	}
	return strings.Join([]string{f.Role, f.Kind, f.Object, f.Via,
		strings.Join(f.Privileges, ","), fix}, "|") + manual
}

func summaries(fs []finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, summary(f))
	}
	return out
}

// TestComputeFindings_CleanHub_ReturnsNone: the owner's grants, plain
// grants to a kept role and intact guards are not findings.
func TestComputeFindings_CleanHub_ReturnsNone(t *testing.T) {
	findings, info := computeFindings(baseCatalog(), keptTeam())
	require.Empty(t, findings)
	require.Empty(t, info)
}

// TestComputeFindings_Grants_RevokeNonKeptRoles covers the grant findings:
// who is revoked, who is left alone, and which statement fixes each.
func TestComputeFindings_Grants_RevokeNonKeptRoles(t *testing.T) {
	tests := []struct {
		name string
		acl  []aclEntry
		want []string
	}{
		{
			name: "PUBLIC on a table",
			acl:  []aclEntry{{obj: keyTable, grantee: publicOID, privilege: "SELECT"}},
			want: []string{"PUBLIC|table|public.audit_log|grant|SELECT|revoke-public TABLE public audit_log"},
		},
		{
			name: "data-API role on a table, privileges sorted",
			acl: []aclEntry{
				{obj: keyTable, grantee: tAnon, privilege: "UPDATE"},
				{obj: keyTable, grantee: tAnon, privilege: "DELETE"},
			},
			want: []string{"anon|table|public.audit_log|grant|DELETE,UPDATE|revoke TABLE public audit_log anon"},
		},
		{
			name: "unrelated role on a sequence",
			acl:  []aclEntry{{obj: keySeq, grantee: tUnrel, privilege: "USAGE"}},
			want: []string{"unrel|sequence|public.audit_log_audit_id_seq|grant|USAGE|revoke SEQUENCE public audit_log_audit_id_seq unrel"},
		},
		{
			name: "PUBLIC execute on a function",
			acl:  []aclEntry{{obj: keyFunc, grantee: publicOID, privilege: "EXECUTE"}},
			want: []string{"PUBLIC|function|public.audit_log_immutable()|grant|EXECUTE|revoke-public-function public audit_log_immutable"},
		},
		{
			name: "kept role holding a grant option",
			acl:  []aclEntry{{obj: keyTable, grantee: tTeam, privilege: "SELECT", grantable: true}},
			want: []string{"team|table|public.audit_log|grant_option|SELECT|revoke-grant-option TABLE public audit_log team"},
		},
		{
			name: "superuser, owner and caller entries are not findings",
			acl: []aclEntry{
				{obj: keyTable, grantee: tSuper, privilege: "SELECT"},
				{obj: keyTable, grantee: tOwner, privilege: "SELECT", grantable: true},
				{obj: keyTable, grantee: tCaller, privilege: "SELECT"},
			},
			want: nil,
		},
		{
			name: "role name outside the identifier form is exposure",
			acl:  []aclEntry{{obj: keyTable, grantee: tBadName, privilege: "SELECT"}},
			want: []string{"Mixed-Case|table|public.audit_log|grant|SELECT|-"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := baseCatalog()
			cat.acl = append(cat.acl, tt.acl...)
			findings, _ := computeFindings(cat, keptTeam())
			if tt.want == nil {
				require.Empty(t, findings)
				return
			}
			require.Equal(t, tt.want, summaries(findings))
		})
	}
}

// TestComputeFindings_BadName_CarriesNote: exposure the command cannot fix
// says why in its note.
func TestComputeFindings_BadName_CarriesNote(t *testing.T) {
	cat := baseCatalog()
	cat.acl = append(cat.acl, aclEntry{obj: keyTable, grantee: tBadName, privilege: "SELECT"})
	findings, _ := computeFindings(cat, keptTeam())
	require.Len(t, findings, 1)
	require.Contains(t, findings[0].Note, "revoke it by hand")
}

// TestComputeFindings_DefaultPrivileges covers default privileges: the
// owner's entries in the sync schema and globally are revoked; another
// creator's entries are reported as information only.
func TestComputeFindings_DefaultPrivileges(t *testing.T) {
	tests := []struct {
		name     string
		defaults []defaultACL
		want     []string
		wantInfo []string
	}{
		{
			name:     "owner in the sync schema",
			defaults: []defaultACL{{creator: tOwner, schema: "public", objType: "r", grantee: tAnon, privilege: "SELECT"}},
			want:     []string{"anon|default_acl|default privileges of owner in schema public on tables|default_acl|SELECT|revoke-default-schema owner public TABLES anon"},
		},
		{
			name:     "owner globally toward PUBLIC on functions",
			defaults: []defaultACL{{creator: tOwner, objType: "f", grantee: publicOID, privilege: "EXECUTE"}},
			want:     []string{"PUBLIC|default_acl|default privileges of owner in all schemas on functions|default_acl|EXECUTE|revoke-default-global-public owner FUNCTIONS"},
		},
		{
			name: "owner's own and kept entries are not findings",
			defaults: []defaultACL{
				{creator: tOwner, objType: "r", grantee: tOwner, privilege: "SELECT"},
				{creator: tOwner, schema: "public", objType: "S", grantee: tTeam, privilege: "USAGE"},
			},
		},
		{
			name:     "another creator is information only",
			defaults: []defaultACL{{creator: tSuper, objType: "r", grantee: tAnon, privilege: "SELECT"}},
			wantInfo: []string{"anon|default_acl|default privileges of postgres in all schemas on tables|default_acl|SELECT|-"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := baseCatalog()
			cat.defaults = tt.defaults
			findings, info := computeFindings(cat, keptTeam())
			if tt.want == nil {
				require.Empty(t, findings)
			} else {
				require.Equal(t, tt.want, summaries(findings))
			}
			if tt.wantInfo == nil {
				require.Empty(t, info)
			} else {
				require.Equal(t, tt.wantInfo, summaries(info))
			}
		})
	}
}

// TestComputeFindings_Memberships covers role memberships: a member of a
// predefined read-all role is revoked when the caller may, or reported with
// the administrator's statement; a member of the owner role is reported.
func TestComputeFindings_Memberships(t *testing.T) {
	t.Run("revocable read-all member", func(t *testing.T) {
		cat := baseCatalog()
		cat.edges = []roleEdge{{role: tReadAll, member: tReader, grantor: tOwner, revocable: true}}
		findings, _ := computeFindings(cat, keptTeam())
		require.Equal(t, []string{"reader|role|pg_read_all_data|membership||revoke-membership pg_read_all_data reader owner"},
			summaries(findings))
		require.Equal(t, "cluster-wide", findings[0].Scope)
	})
	t.Run("read-all member the caller cannot revoke", func(t *testing.T) {
		cat := baseCatalog()
		cat.edges = []roleEdge{{role: tReadAll, member: tReader, grantor: tSuper}}
		findings, _ := computeFindings(cat, keptTeam())
		require.Equal(t, []string{"reader|role|pg_read_all_data|membership||-|manual:manual-revoke-membership pg_read_all_data reader"},
			summaries(findings))
	})
	t.Run("kept member is left alone", func(t *testing.T) {
		cat := baseCatalog()
		cat.edges = []roleEdge{{role: tReadAll, member: tTeam, grantor: tOwner, revocable: true}}
		findings, _ := computeFindings(cat, keptTeam())
		require.Empty(t, findings)
	})
	t.Run("member of the owner role is reported, never changed", func(t *testing.T) {
		cat := baseCatalog()
		cat.usage[[2]uint32{tCarol, tOwner}] = true
		cat.effective = []effPriv{{role: tCarol, obj: keyTable, privilege: "DELETE"}}
		findings, _ := computeFindings(cat, keptTeam())
		require.Equal(t, []string{"carol|role|owner|owner_membership||-"}, summaries(findings))
	})
}

// TestComputeFindings_EffectivePrivileges covers access that no direct
// grant explains: membership of a kept role is accepted, access through a
// revoked grant or membership is not repeated, and anything else is
// reported as membership exposure.
func TestComputeFindings_EffectivePrivileges(t *testing.T) {
	t.Run("member of a kept role", func(t *testing.T) {
		cat := baseCatalog()
		cat.usage[[2]uint32{tAlice, tTeam}] = true
		cat.effective = []effPriv{
			{role: tTeam, obj: keyTable, privilege: "SELECT"},
			{role: tAlice, obj: keyTable, privilege: "SELECT"},
		}
		findings, _ := computeFindings(cat, keptTeam())
		require.Empty(t, findings)
	})
	t.Run("member of a non-kept group is covered by the group's revoke", func(t *testing.T) {
		cat := baseCatalog()
		cat.acl = append(cat.acl, aclEntry{obj: keyTable, grantee: tGroup, privilege: "SELECT"})
		cat.usage[[2]uint32{tBob, tGroup}] = true
		cat.effective = []effPriv{
			{role: tGroup, obj: keyTable, privilege: "SELECT"},
			{role: tBob, obj: keyTable, privilege: "SELECT"},
		}
		findings, _ := computeFindings(cat, keptTeam())
		require.Equal(t, []string{"grp|table|public.audit_log|grant|SELECT|revoke TABLE public audit_log grp"},
			summaries(findings))
	})
	t.Run("access through PUBLIC is reported once, as PUBLIC", func(t *testing.T) {
		cat := baseCatalog()
		cat.acl = append(cat.acl, aclEntry{obj: keyTable, grantee: publicOID, privilege: "SELECT"})
		cat.effective = []effPriv{{role: tUnrel, obj: keyTable, privilege: "SELECT"}}
		findings, _ := computeFindings(cat, keptTeam())
		require.Equal(t, []string{"PUBLIC|table|public.audit_log|grant|SELECT|revoke-public TABLE public audit_log"},
			summaries(findings))
	})
	t.Run("member of a revocable read-all role is covered by the membership", func(t *testing.T) {
		cat := baseCatalog()
		cat.edges = []roleEdge{{role: tReadAll, member: tReader, grantor: tOwner, revocable: true}}
		cat.effective = []effPriv{{role: tReader, obj: keyTable, privilege: "SELECT"}}
		findings, _ := computeFindings(cat, keptTeam())
		require.Len(t, findings, 1)
		require.Equal(t, FindingViaMembership, findings[0].Via)
		require.Equal(t, "pg_read_all_data", findings[0].Object)
	})
	t.Run("unexplained access is membership exposure", func(t *testing.T) {
		cat := baseCatalog()
		cat.effective = []effPriv{
			{role: tUnrel, obj: keyTable, privilege: "SELECT"},
			{role: tUnrel, obj: keyTable, privilege: "INSERT"},
		}
		findings, _ := computeFindings(cat, keptTeam())
		require.Equal(t, []string{"unrel|table|public.audit_log|membership|INSERT,SELECT|-"}, summaries(findings))
	})
	t.Run("predefined, superuser, owner, caller and kept roles are not checked", func(t *testing.T) {
		cat := baseCatalog()
		cat.effective = []effPriv{
			{role: tReadAll, obj: keyTable, privilege: "SELECT"},
			{role: tSuper, obj: keyTable, privilege: "SELECT"},
			{role: tOwner, obj: keyTable, privilege: "SELECT"},
			{role: tCaller, obj: keyTable, privilege: "SELECT"},
			{role: tTeam, obj: keyTable, privilege: "SELECT"},
		}
		findings, _ := computeFindings(cat, keptTeam())
		require.Empty(t, findings)
	})
}

// TestComputeFindings_Guards covers the TRUNCATE guards: a missing guard is
// restored by the guard migration, a disabled one is enabled, and one that
// calls another function is reported.
func TestComputeFindings_Guards(t *testing.T) {
	tests := []struct {
		name  string
		guard guardState
		want  []string
	}{
		{"enabled always", guardState{table: "audit_log", trigger: "audit_log_no_truncate", enabled: "A", function: guardFunction}, nil},
		{"missing", guardState{table: "audit_log", trigger: "audit_log_no_truncate"},
			[]string{"|trigger|audit_log_no_truncate on public.audit_log|missing||migration 016_append_only_truncate_guard.sql"}},
		{"disabled", guardState{table: "audit_log", trigger: "audit_log_no_truncate", enabled: "D", function: guardFunction},
			[]string{"|trigger|audit_log_no_truncate on public.audit_log|disabled||enable public audit_log audit_log_no_truncate"}},
		{"replica only", guardState{table: "audit_log", trigger: "audit_log_no_truncate", enabled: "R", function: guardFunction},
			[]string{"|trigger|audit_log_no_truncate on public.audit_log|disabled||enable public audit_log audit_log_no_truncate"}},
		{"another function", guardState{table: "audit_log", trigger: "audit_log_no_truncate", enabled: "O", function: "other_fn"},
			[]string{"|trigger|audit_log_no_truncate on public.audit_log|missing||-"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := baseCatalog()
			cat.guards = []guardState{tt.guard}
			findings, _ := computeFindings(cat, keptTeam())
			if tt.want == nil {
				require.Empty(t, findings)
				return
			}
			require.Equal(t, tt.want, summaries(findings))
		})
	}
}

// TestPlanActions_Order_RevokesGrantOptionsFirst: grant options are revoked
// before plain grants, so a CASCADE removes every regrant made through
// them; guards come last.
func TestPlanActions_Order_RevokesGrantOptionsFirst(t *testing.T) {
	cat := baseCatalog()
	cat.acl = append(cat.acl,
		aclEntry{obj: keyTable, grantee: tAnon, privilege: "SELECT"},
		aclEntry{obj: keyTable, grantee: publicOID, privilege: "SELECT"},
		aclEntry{obj: keyTable, grantee: tTeam, privilege: "UPDATE", grantable: true},
	)
	cat.defaults = []defaultACL{{creator: tOwner, schema: "public", objType: "r", grantee: tAnon, privilege: "SELECT"}}
	cat.edges = []roleEdge{{role: tReadAll, member: tReader, grantor: tOwner, revocable: true}}
	cat.guards = []guardState{{table: "audit_log", trigger: "audit_log_no_truncate"}}
	findings, _ := computeFindings(cat, keptTeam())

	var shapes []string
	for _, a := range planActions(findings) {
		shapes = append(shapes, a.shape())
	}
	require.Equal(t, []string{
		"revoke-grant-option TABLE public audit_log team",
		"revoke TABLE public audit_log anon",
		"revoke-public TABLE public audit_log",
		"revoke-default-schema owner public TABLES anon",
		"revoke-membership pg_read_all_data reader owner",
		"migration 016_append_only_truncate_guard.sql",
	}, shapes)
}

// TestAction_Args_RejectsUnexpectedValues: every value bound into a
// format() template is an allowed keyword or a valid identifier.
func TestAction_Args_RejectsUnexpectedValues(t *testing.T) {
	ok := action{query: sqlFmtRevokeRelation, params: []string{"TABLE", "public", "audit_log", "anon"}}
	args, err := ok.args()
	require.NoError(t, err)
	require.Equal(t, []any{"TABLE", "public", "audit_log", "anon"}, args)

	for _, bad := range []action{
		{query: sqlFmtRevokeRelation, params: []string{"TABLE; DROP", "public", "audit_log", "anon"}},
		{query: sqlFmtRevokeRelation, params: []string{"TABLE", "public", "audit_log", `an"on`}},
		{query: sqlFmtRevokeFunctionPublic, params: []string{"Public", "f"}},
		{query: sqlFmtRevokeFunctionPublic, params: []string{"public", ""}},
	} {
		_, err := bad.args()
		require.Error(t, err)
	}
}
