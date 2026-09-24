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

	tPlainFunc uint32 = 30004 // a function that does not return trigger
)

var (
	keyTable = objKey{cat: catRelation, oid: tTable}
	keySeq   = objKey{cat: catRelation, oid: tSeq}
	keyFunc  = objKey{cat: catFunction, oid: tFunc}

	keyPlainFunc = objKey{cat: catFunction, oid: tPlainFunc}
)

// baseCatalog is a clean hub: the owner's own grants, one kept team role
// with plain grants, and intact guards.
func baseCatalog() *hubCatalog {
	return &hubCatalog{
		schema:        "public",
		serverVersion: 160000,
		current:       tCaller,
		owners:        map[uint32]bool{tOwner: true},
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
			{key: keyFunc, kind: FindingKindFunction, schema: "public", name: "audit_log_immutable", owner: tOwner, trigger: true},
			{key: keyPlainFunc, kind: FindingKindFunction, schema: "public", name: "plain_fn", owner: tOwner},
		},
		acl: []aclEntry{
			{obj: keyTable, grantee: tOwner, grantor: tOwner, privilege: "SELECT"},
			{obj: keyTable, grantee: tTeam, grantor: tOwner, privilege: "SELECT"},
			{obj: keyTable, grantee: tTeam, grantor: tOwner, privilege: "INSERT"},
			{obj: keySeq, grantee: tTeam, grantor: tOwner, privilege: "USAGE"},
		},
		usage:  map[[2]uint32]bool{},
		member: map[[2]uint32]bool{},
		canSet: map[[2]uint32]bool{},
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
			name: "PUBLIC execute on a function that is not a trigger function",
			acl:  []aclEntry{{obj: keyPlainFunc, grantee: publicOID, privilege: "EXECUTE"}},
			want: []string{"PUBLIC|function|public.plain_fn()|grant|EXECUTE|revoke-public-function public plain_fn"},
		},
		{
			name: "column privileges of a non-kept role",
			acl: []aclEntry{
				{obj: keyTable, grantee: tAnon, privilege: "SELECT", column: "payload"},
				{obj: keyTable, grantee: tAnon, privilege: "SELECT", column: "event_id"},
				{obj: keyTable, grantee: tAnon, privilege: "UPDATE", column: "payload"},
			},
			want: []string{"anon|table|public.audit_log|grant|SELECT (event_id, payload),UPDATE (payload)|revoke TABLE public audit_log anon"},
		},
		{
			name: "column and table privileges together",
			acl: []aclEntry{
				{obj: keyTable, grantee: tUnrel, privilege: "SELECT"},
				{obj: keyTable, grantee: tUnrel, privilege: "UPDATE", column: "actor"},
			},
			want: []string{"unrel|table|public.audit_log|grant|SELECT,UPDATE (actor)|revoke TABLE public audit_log unrel"},
		},
		{
			name: "kept role holding a column grant option",
			acl:  []aclEntry{{obj: keyTable, grantee: tTeam, grantor: tOwner, privilege: "SELECT", column: "payload", grantable: true}},
			want: []string{"team|table|public.audit_log|grant_option|SELECT (payload)|revoke-grant-option TABLE public audit_log team"},
		},
		{
			name: "kept role holding a grant option",
			acl:  []aclEntry{{obj: keyTable, grantee: tTeam, grantor: tOwner, privilege: "SELECT", grantable: true}},
			want: []string{"team|table|public.audit_log|grant_option|SELECT|revoke-grant-option TABLE public audit_log team"},
		},
		{
			name: "superuser and owner entries are not findings",
			acl: []aclEntry{
				{obj: keyTable, grantee: tSuper, privilege: "SELECT"},
				{obj: keyTable, grantee: tOwner, privilege: "SELECT", grantable: true},
			},
			want: nil,
		},
		{
			name: "the connecting role is checked like any role",
			acl:  []aclEntry{{obj: keyTable, grantee: tCaller, privilege: "SELECT"}},
			want: []string{"caller|table|public.audit_log|grant|SELECT|revoke TABLE public audit_log caller"},
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
		cat.member[[2]uint32{tCarol, tOwner}] = true
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
	t.Run("predefined, superuser, owner and kept roles are not checked", func(t *testing.T) {
		cat := baseCatalog()
		cat.effective = []effPriv{
			{role: tReadAll, obj: keyTable, privilege: "SELECT"},
			{role: tSuper, obj: keyTable, privilege: "SELECT"},
			{role: tOwner, obj: keyTable, privilege: "SELECT"},
			{role: tTeam, obj: keyTable, privilege: "SELECT"},
		}
		findings, _ := computeFindings(cat, keptTeam())
		require.Empty(t, findings)
	})
	t.Run("the connecting role is checked", func(t *testing.T) {
		cat := baseCatalog()
		cat.effective = []effPriv{{role: tCaller, obj: keyTable, privilege: "SELECT"}}
		findings, _ := computeFindings(cat, keptTeam())
		require.Equal(t, []string{"caller|table|public.audit_log|membership|SELECT|-"}, summaries(findings))
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
		aclEntry{obj: keyTable, grantee: tTeam, grantor: tOwner, privilege: "UPDATE", grantable: true},
	)
	cat.defaults = []defaultACL{{creator: tOwner, schema: "public", objType: "r", grantee: tAnon, privilege: "SELECT"}}
	cat.edges = []roleEdge{{role: tReadAll, member: tReader, grantor: tOwner, revocable: true}}
	cat.guards = []guardState{{table: "audit_log", trigger: "audit_log_no_truncate"}}
	findings, _ := computeFindings(cat, keptTeam())

	var shapes []string
	for _, a := range planActions(findings, nil) {
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

// TestComputeFindings_TriggerFunctionExecute_IsInformation: EXECUTE on an
// mtix trigger function is information, not a finding: a trigger function
// cannot be called directly and triggers fire without EXECUTE. A hub whose
// only item is that verifies clean, and --apply plans nothing; when --apply
// runs for other findings it revokes it too (MTIX-95.1).
func TestComputeFindings_TriggerFunctionExecute_IsInformation(t *testing.T) {
	cat := baseCatalog()
	cat.acl = append(cat.acl,
		aclEntry{obj: keyFunc, grantee: publicOID, privilege: "EXECUTE"},
		aclEntry{obj: keyFunc, grantee: tAnon, privilege: "EXECUTE"},
	)
	cat.effective = []effPriv{{role: tUnrel, obj: keyFunc, privilege: "EXECUTE"}}
	findings, info := computeFindings(cat, keptTeam())
	require.Empty(t, findings, "trigger-function EXECUTE is not a finding")
	require.Equal(t, []string{
		"PUBLIC|function|public.audit_log_immutable()|grant|EXECUTE|revoke-public-function public audit_log_immutable",
		"anon|function|public.audit_log_immutable()|grant|EXECUTE|revoke-function public audit_log_immutable anon",
	}, summaries(info))
	for _, f := range info {
		require.Contains(t, f.Note, "trigger function")
	}
	require.Empty(t, planActions(findings, info), "nothing to apply when nothing fails")

	cat.acl = append(cat.acl, aclEntry{obj: keyTable, grantee: tAnon, privilege: "SELECT"})
	findings, info = computeFindings(cat, keptTeam())
	var shapes []string
	for _, a := range planActions(findings, info) {
		shapes = append(shapes, a.shape())
	}
	require.Equal(t, []string{
		"revoke TABLE public audit_log anon",
		"revoke-function public audit_log_immutable anon",
		"revoke-public-function public audit_log_immutable",
	}, shapes, "with other changes, --apply also revokes trigger-function EXECUTE")
}

// TestComputeFindings_KeptMember_ExtraPrivilegeReported: membership of a
// kept role explains only the privileges the kept role itself holds.
func TestComputeFindings_KeptMember_ExtraPrivilegeReported(t *testing.T) {
	cat := baseCatalog()
	cat.usage[[2]uint32{tAlice, tTeam}] = true
	cat.member[[2]uint32{tAlice, tTeam}] = true
	cat.effective = []effPriv{
		{role: tTeam, obj: keyTable, privilege: "SELECT"},
		{role: tAlice, obj: keyTable, privilege: "SELECT"},
		{role: tAlice, obj: keyTable, privilege: "DELETE"},
	}
	findings, _ := computeFindings(cat, keptTeam())
	require.Equal(t, []string{"alice|table|public.audit_log|membership|DELETE|-"}, summaries(findings))
}

// TestComputeFindings_OwnerMembership_SetOnlyMemberReported: a member of
// the owner role that can SET ROLE to it, without inheriting, holds the
// owner's privileges on demand and is reported like an inheriting member.
func TestComputeFindings_OwnerMembership_SetOnlyMemberReported(t *testing.T) {
	cat := baseCatalog()
	cat.member[[2]uint32{tCarol, tOwner}] = true // SET only: no usage pair
	findings, _ := computeFindings(cat, keptTeam())
	require.Equal(t, []string{"carol|role|owner|owner_membership||-"}, summaries(findings))
}

// TestComputeFindings_MemberOfReadAllMember_Reported: a role that inherits
// or can SET ROLE to a direct member of a read-all role is reported too,
// with the same fix as that member, which --apply runs once.
func TestComputeFindings_MemberOfReadAllMember_Reported(t *testing.T) {
	cat := baseCatalog()
	cat.edges = []roleEdge{{role: tReadAll, member: tGroup, grantor: tOwner, revocable: true}}
	cat.member[[2]uint32{tBob, tGroup}] = true // SET only
	findings, _ := computeFindings(cat, keptTeam())
	require.Equal(t, []string{
		"bob|role|pg_read_all_data|membership||revoke-membership pg_read_all_data grp owner",
		"grp|role|pg_read_all_data|membership||revoke-membership pg_read_all_data grp owner",
	}, summaries(findings))
	require.Contains(t, findings[0].Note, "through grp")
	require.Len(t, planActions(findings, nil), 1, "one revoke covers both")
}

// TestComputeFindings_ObjectOwnedByAnotherRole_Reported: an mtix function
// or sequence owned by a role other than the sync tables' owner is
// reported with the administrator's statement that returns it: its owner
// could replace a trigger function's body. A superuser owner is out of
// scope (MTIX-95.1).
func TestComputeFindings_ObjectOwnedByAnotherRole_Reported(t *testing.T) {
	cat := baseCatalog()
	cat.objects[2].owner = tUnrel // audit_log_immutable()
	cat.objects[1].owner = tSuper // the sequence
	findings, _ := computeFindings(cat, keptTeam())
	require.Equal(t, []string{
		"unrel|function|public.audit_log_immutable()|object_owner||-|manual:manual-owner-function public audit_log_immutable owner",
	}, summaries(findings))
	require.Contains(t, findings[0].Note, "owned by")
}

// TestComputeFindings_TriggerFunctionEffectiveExecute_NotReported: EXECUTE
// on an mtix trigger function that a role holds through membership alone
// is not access either (MTIX-95.1).
func TestComputeFindings_TriggerFunctionEffectiveExecute_NotReported(t *testing.T) {
	cat := baseCatalog()
	cat.effective = []effPriv{
		{role: tUnrel, obj: keyFunc, privilege: "EXECUTE"},
		{role: tUnrel, obj: keyPlainFunc, privilege: "EXECUTE"},
	}
	findings, _ := computeFindings(cat, keptTeam())
	require.Equal(t, []string{"unrel|function|public.plain_fn()|membership|EXECUTE|-"}, summaries(findings),
		"only the function that is not a trigger function is reported")
}

// TestComputeFindings_KeptRoleGrantedByOtherRole_Regranted: a kept role's
// privilege granted by a role other than the owner would vanish with that
// role's CASCADE revoke, so --apply grants it again from the owner first,
// column privileges included (MTIX-95.1).
func TestComputeFindings_KeptRoleGrantedByOtherRole_Regranted(t *testing.T) {
	cat := baseCatalog()
	cat.acl = append(cat.acl,
		aclEntry{obj: keyTable, grantee: tUnrel, grantor: tOwner, privilege: "SELECT", grantable: true},
		aclEntry{obj: keyTable, grantee: tTeam, grantor: tUnrel, privilege: "DELETE"},
		aclEntry{obj: keyTable, grantee: tTeam, grantor: tUnrel, privilege: "UPDATE", column: "actor"},
		aclEntry{obj: keySeq, grantee: tTeam, grantor: tOwner, privilege: "SELECT"},
	)
	findings, _ := computeFindings(cat, keptTeam())
	require.Equal(t, []string{
		"team|table|public.audit_log|grantor|DELETE|regrant DELETE TABLE public audit_log team",
		"team|table|public.audit_log|grantor|UPDATE (actor)|regrant-column UPDATE actor public audit_log team",
		"unrel|table|public.audit_log|grant|SELECT|revoke TABLE public audit_log unrel",
	}, summaries(findings))
	require.Contains(t, findings[0].Note, "granted by unrel")

	var shapes []string
	for _, a := range planActions(findings, nil) {
		shapes = append(shapes, a.shape())
	}
	require.Equal(t, []string{
		"regrant DELETE TABLE public audit_log team",
		"regrant-column UPDATE actor public audit_log team",
		"revoke TABLE public audit_log unrel",
	}, shapes, "the owner grants again before the CASCADE revoke")
}

// TestComputeFindings_GrantToPredefinedRole_Reported: a grant to a
// predefined role such as pg_monitor is revoked like any other role's
// (MTIX-95.1).
func TestComputeFindings_GrantToPredefinedRole_Reported(t *testing.T) {
	cat := baseCatalog()
	const tMonitor uint32 = 3373
	cat.roles[tMonitor] = roleInfo{oid: tMonitor, name: "pg_monitor"}
	cat.acl = append(cat.acl, aclEntry{obj: keyTable, grantee: tMonitor, privilege: "SELECT"})
	findings, _ := computeFindings(cat, keptTeam())
	require.Equal(t, []string{"pg_monitor|table|public.audit_log|grant|SELECT|revoke TABLE public audit_log pg_monitor"},
		summaries(findings))
}

// TestComputeFindings_CreateRoleBeforePG16_Reported: before PostgreSQL 16 a
// CREATEROLE role can grant itself any role that is not a superuser, the
// owner included, so it is reported; from 16 on, the membership checks
// cover what it can reach (MTIX-95.1).
func TestComputeFindings_CreateRoleBeforePG16_Reported(t *testing.T) {
	for _, tt := range []struct {
		version int
		want    []string
	}{
		{150000, []string{"unrel|role|CREATEROLE|createrole||-"}},
		{160000, nil},
	} {
		cat := baseCatalog()
		cat.serverVersion = tt.version
		r := cat.roles[tUnrel]
		r.createRole = true
		cat.roles[tUnrel] = r
		findings, _ := computeFindings(cat, keptTeam())
		if tt.want == nil {
			require.Empty(t, findings, "version %d", tt.version)
			continue
		}
		require.Equal(t, tt.want, summaries(findings), "version %d", tt.version)
	}
}

// TestComputeFindings_RegrantSkipsObjectOwnedByAnotherRole: the owner
// cannot grant on an object another role owns, so no re-grant is planned
// there; the object_owner finding reports it instead (MTIX-95.1).
func TestComputeFindings_RegrantSkipsObjectOwnedByAnotherRole(t *testing.T) {
	cat := baseCatalog()
	cat.objects[1].owner = tUnrel // the sequence
	cat.acl = append(cat.acl, aclEntry{obj: keySeq, grantee: tTeam, grantor: tUnrel, privilege: "SELECT"})
	findings, _ := computeFindings(cat, keptTeam())
	for _, f := range findings {
		require.NotEqual(t, FindingViaGrantor, f.Via, "%+v", f.PrivilegeFinding)
	}
}

// TestComputeFindings_EscalationRoles_Reported: a checked role that can
// SET ROLE to a superuser, or that is a member of a server-file or
// server-program role in any way, is reported; one that only inherits a
// superuser's grants is not reported as a superuser (MTIX-95.1).
func TestComputeFindings_EscalationRoles_Reported(t *testing.T) {
	const tSuper2, tExec, tReadFiles uint32 = 20050, 4571, 4569
	cat := baseCatalog()
	cat.roles[tSuper2] = roleInfo{oid: tSuper2, name: "admin_su", super: true}
	cat.roles[tExec] = roleInfo{oid: tExec, name: "pg_execute_server_program"}
	cat.roles[tReadFiles] = roleInfo{oid: tReadFiles, name: "pg_read_server_files"}
	cat.canSet[[2]uint32{tUnrel, tSuper2}] = true
	cat.member[[2]uint32{tUnrel, tSuper2}] = true
	cat.member[[2]uint32{tBob, tSuper2}] = true // inherits only: no SET
	cat.usage[[2]uint32{tBob, tSuper2}] = true
	cat.member[[2]uint32{tReader, tExec}] = true
	cat.member[[2]uint32{tAlice, tReadFiles}] = true // ADMIN only
	findings, _ := computeFindings(cat, keptTeam())
	require.Equal(t, []string{
		"alice|role|pg_read_server_files|membership||-",
		"reader|role|pg_execute_server_program|membership||-",
		"unrel|role|admin_su|superuser_membership||-",
	}, summaries(findings))
}

// TestCallerScope_Roles_SaysWhetherChecked: the report states whether the
// connecting role was checked (MTIX-95.1).
func TestCallerScope_Roles_SaysWhetherChecked(t *testing.T) {
	tests := []struct {
		name   string
		caller uint32
		want   string
	}{
		{"owner", tOwner, CallerOwner},
		{"superuser", tSuper, CallerSuperuser},
		{"kept", tTeam, CallerKept},
		{"any other role", tCaller, CallerChecked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := baseCatalog()
			cat.current = tt.caller
			cat.super = cat.roles[tt.caller].super
			require.Equal(t, tt.want, cat.callerScope(keptTeam()))
		})
	}
}
