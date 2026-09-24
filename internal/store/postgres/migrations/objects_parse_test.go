// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseObjects_StatementForms_ReturnsObjects covers the statement forms
// the migration parser must read (MTIX-95.1): plain and IF NOT EXISTS
// tables, OR REPLACE functions, top-level triggers and triggers created
// inside a DO block, with comments ignored.
func TestParseObjects_StatementForms_ReturnsObjects(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		tables    []string
		functions []string
		triggers  []Trigger
	}{
		{
			name:   "plain and if-not-exists tables",
			body:   "CREATE TABLE a (id INT);\ncreate table if not exists B (id INT);",
			tables: []string{"a", "b"},
		},
		{
			name:      "function with or replace",
			body:      "CREATE OR REPLACE FUNCTION f_one()\nRETURNS TRIGGER AS $$ BEGIN END; $$ LANGUAGE plpgsql;\nCREATE FUNCTION f_two() RETURNS INT AS $$ SELECT 1 $$ LANGUAGE sql;",
			functions: []string{"f_one", "f_two"},
		},
		{
			name:     "top-level trigger over several lines",
			body:     "CREATE TRIGGER t_upd\n    BEFORE UPDATE ON tab\n    FOR EACH ROW EXECUTE FUNCTION f();",
			triggers: []Trigger{{Name: "t_upd", Table: "tab", Event: "UPDATE"}},
		},
		{
			name: "trigger inside a DO block",
			body: "DO $$\nBEGIN\n  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'g') THEN\n" +
				"    CREATE TRIGGER g\n      BEFORE TRUNCATE ON tab\n      FOR EACH STATEMENT EXECUTE FUNCTION f();\n  END IF;\nEND\n$$;",
			triggers: []Trigger{{Name: "g", Table: "tab", Event: "TRUNCATE"}},
		},
		{
			name:     "drop trigger is not a creation",
			body:     "DROP TRIGGER IF EXISTS t_old ON tab;",
			triggers: nil,
		},
		{
			name:   "comments are ignored",
			body:   "-- CREATE TABLE not_real (id INT);\nCREATE TABLE real_one (id INT); -- CREATE TABLE also_not_real",
			tables: []string{"real_one"},
		},
		{
			name:   "same table named twice is listed once",
			body:   "CREATE TABLE IF NOT EXISTS a (id INT);\nCREATE TABLE IF NOT EXISTS a (id INT);",
			tables: []string{"a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseObjects([]string{tt.body})
			require.Equal(t, tt.tables, got.tables)
			require.Equal(t, tt.functions, got.functions)
			require.Equal(t, tt.triggers, got.triggers)
		})
	}
}

// TestParseObjects_SeveralFiles_SortsAndMerges checks that objects from
// several migration bodies merge into one sorted, de-duplicated set.
func TestParseObjects_SeveralFiles_SortsAndMerges(t *testing.T) {
	got := parseObjects([]string{
		"CREATE TABLE zeta (id INT);\nCREATE TRIGGER z_t BEFORE DELETE ON zeta FOR EACH ROW EXECUTE FUNCTION f();",
		"CREATE TABLE alpha (id INT);\nCREATE TRIGGER a_t AFTER INSERT ON alpha FOR EACH ROW EXECUTE FUNCTION f();",
	})
	require.Equal(t, []string{"alpha", "zeta"}, got.tables)
	require.Equal(t, []Trigger{
		{Name: "a_t", Table: "alpha", Event: "INSERT"},
		{Name: "z_t", Table: "zeta", Event: "DELETE"},
	}, got.triggers)
}
