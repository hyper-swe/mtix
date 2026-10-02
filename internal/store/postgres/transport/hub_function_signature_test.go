// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// tRecorder is an mtix function with arguments in the synthetic catalog.
const tRecorder uint32 = 30005

var keyRecorder = objKey{cat: catFunction, oid: tRecorder}

// recorderCatalog is baseCatalog plus a function that takes arguments and
// does not return trigger, as record_restore_collision (MTIX-95.1.7).
func recorderCatalog() *hubCatalog {
	cat := baseCatalog()
	cat.objects = append(cat.objects, hubObject{key: keyRecorder, kind: FindingKindFunction, schema: "public",
		name: "record_restore_collision", args: "text, bigint", owner: tOwner})
	return cat
}

// TestComputeFindings_FunctionWithArguments_NamedBySignature: a function
// that takes arguments is shown, revoked, granted again and returned to the
// owner by its full signature, and a kept role's plain EXECUTE on it is
// not a finding (MTIX-95.1.7). A kept role's EXECUTE granted by another
// role is granted again from the owner before that role's CASCADE revoke,
// except on a trigger function, whose EXECUTE is not needed.
func TestComputeFindings_FunctionWithArguments_NamedBySignature(t *testing.T) {
	const label = "public.record_restore_collision(text, bigint)"
	tests := []struct {
		name   string
		mutate func(c *hubCatalog)
		want   []string
	}{
		{"PUBLIC holds EXECUTE", func(c *hubCatalog) {
			c.acl = append(c.acl, aclEntry{obj: keyRecorder, grantee: publicOID, grantor: tOwner, privilege: "EXECUTE"})
		}, []string{"PUBLIC|function|" + label + "|grant|EXECUTE|revoke-public-function public record_restore_collision (text, bigint)"}},
		{"a role that is not kept holds EXECUTE", func(c *hubCatalog) {
			c.acl = append(c.acl, aclEntry{obj: keyRecorder, grantee: tAnon, grantor: tOwner, privilege: "EXECUTE"})
		}, []string{"anon|function|" + label + "|grant|EXECUTE|revoke-function public record_restore_collision anon (text, bigint)"}},
		{"a kept role can grant EXECUTE on", func(c *hubCatalog) {
			c.acl = append(c.acl, aclEntry{obj: keyRecorder, grantee: tTeam, grantor: tOwner, privilege: "EXECUTE", grantable: true})
		}, []string{"team|function|" + label + "|grant_option|EXECUTE|revoke-grant-option-function public record_restore_collision team (text, bigint)"}},
		{"a kept role holds EXECUTE by another role's grant", func(c *hubCatalog) {
			c.acl = append(c.acl, aclEntry{obj: keyRecorder, grantee: tTeam, grantor: tUnrel, privilege: "EXECUTE"})
		}, []string{"team|function|" + label + "|grantor|EXECUTE|regrant-function public record_restore_collision team (text, bigint)"}},
		{"another role owns it", func(c *hubCatalog) { c.objects[len(c.objects)-1].owner = tUnrel },
			[]string{"unrel|function|" + label + "|object_owner||-|manual:manual-owner-function public record_restore_collision owner (text, bigint)"}},
		{"a kept role's own EXECUTE", func(c *hubCatalog) {
			c.acl = append(c.acl, aclEntry{obj: keyRecorder, grantee: tTeam, grantor: tOwner, privilege: "EXECUTE"})
		}, nil},
		{"a kept role's trigger-function EXECUTE by another role's grant", func(c *hubCatalog) {
			c.acl = append(c.acl, aclEntry{obj: keyFunc, grantee: tTeam, grantor: tUnrel, privilege: "EXECUTE"})
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := recorderCatalog()
			tt.mutate(cat)
			findings, _ := computeFindings(cat, keptTeam())
			if tt.want == nil {
				require.Empty(t, findings, summaries(findings))
				return
			}
			require.Equal(t, tt.want, summaries(findings))
		})
	}
}

// TestAction_Args_FunctionArgumentTypes: a function statement binds its
// argument types last, as a list of lower-case type names separated by
// ", ", or empty for a function without arguments; any other form is
// refused, so it is never placed in a statement (MTIX-95.1.7).
func TestAction_Args_FunctionArgumentTypes(t *testing.T) {
	ok := newFunctionAction("revoke-function", sqlFmtRevokeFunction, rankRoleGrant, "text, bigint", "public", "f", "anon")
	require.NotNil(t, ok)
	args, err := ok.args()
	require.NoError(t, err)
	require.Equal(t, []any{"public", "f", "anon", "text, bigint"}, args)
	require.Equal(t, "revoke-function public f anon (text, bigint)", ok.shape())

	none := newFunctionAction("revoke-function", sqlFmtRevokeFunction, rankRoleGrant, "", "public", "f", "anon")
	require.NotNil(t, none)
	args, err = none.args()
	require.NoError(t, err)
	require.Equal(t, []any{"public", "f", "anon", ""}, args)
	require.Equal(t, "revoke-function public f anon", none.shape())

	for _, bad := range []string{"text); DROP TABLE audit_log; --", "Text", "text,bigint", "double precision",
		"text, ", "text, bigint)", `"text"`} {
		require.Nilf(t, newFunctionAction("revoke-function", sqlFmtRevokeFunction, rankRoleGrant, bad, "public", "f", "anon"),
			"%q is refused", bad)
	}
}
