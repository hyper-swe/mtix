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
			triggers: []Trigger{{Name: "t_upd", Table: "tab", Event: "UPDATE", Function: "f"}},
		},
		{
			name: "trigger inside a DO block",
			body: "DO $$\nBEGIN\n  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'g') THEN\n" +
				"    CREATE TRIGGER g\n      BEFORE TRUNCATE ON tab\n      FOR EACH STATEMENT EXECUTE FUNCTION f();\n  END IF;\nEND\n$$;",
			triggers: []Trigger{{Name: "g", Table: "tab", Event: "TRUNCATE", Function: "f"}},
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

// TestParseObjects_SerialColumns_ReturnsSequences: each serial column of a
// created table yields the sequence PostgreSQL creates for it,
// <table>_<column>_seq, for serial, serial2, serial4, serial8, bigserial
// and smallserial in any case, with the column name folded to lower case;
// a column of another type, a name that only starts with "serial", a
// serial named only in a comment, and a serial column of a later table
// are not attributed to an earlier one (MTIX-95.1.4).
func TestParseObjects_SerialColumns_ReturnsSequences(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		sequences []string
	}{
		{"every serial form", "CREATE TABLE IF NOT EXISTS t (\n  id BIGSERIAL PRIMARY KEY,\n  n serial,\n  s SmallSerial,\n  x BIGINT\n);",
			[]string{"t_id_seq", "t_n_seq", "t_s_seq"}},
		{"serial2, serial4 and serial8", "CREATE TABLE t (a serial2, b SERIAL4, c Serial8);",
			[]string{"t_a_seq", "t_b_seq", "t_c_seq"}},
		{"an upper-case column name", "CREATE TABLE t (Ticket_ID BIGSERIAL PRIMARY KEY);", []string{"t_ticket_id_seq"}},
		{"names that only start with serial", "CREATE TABLE t (\n  id INT PRIMARY KEY,\n  serial_no TEXT,\n" +
			"  owner_id INT REFERENCES serial_owners (id)\n);", nil},
		{"no serial column", "CREATE TABLE t (id BIGINT PRIMARY KEY);", nil},
		{"a serial in a comment", "CREATE TABLE t (\n  id BIGINT -- was id BIGSERIAL\n);", nil},
		{"each table keeps its own columns", "CREATE TABLE a (id INT);\nCREATE INDEX a_i ON a (id);\nCREATE TABLE b (k BIGSERIAL);",
			[]string{"b_k_seq"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.sequences, parseObjects([]string{tt.body}).sequences)
		})
	}
}

// TestParseObjects_SeveralFiles_SortsAndMerges checks that objects from
// several migration bodies merge into one sorted, de-duplicated set, and
// that each trigger records the function it executes, under EXECUTE
// FUNCTION or EXECUTE PROCEDURE, folded to lower case (MTIX-95.7).
func TestParseObjects_SeveralFiles_SortsAndMerges(t *testing.T) {
	got := parseObjects([]string{
		"CREATE TABLE zeta (id INT);\nCREATE TRIGGER z_t BEFORE DELETE ON zeta FOR EACH ROW EXECUTE PROCEDURE G_Fn();",
		"CREATE TABLE alpha (id INT);\nCREATE TRIGGER a_t AFTER INSERT ON alpha FOR EACH ROW EXECUTE FUNCTION f();",
	})
	require.Equal(t, []string{"alpha", "zeta"}, got.tables)
	require.Equal(t, []Trigger{
		{Name: "a_t", Table: "alpha", Event: "INSERT", Function: "f"},
		{Name: "z_t", Table: "zeta", Event: "DELETE", Function: "g_fn"},
	}, got.triggers)
}

// TestParseObjects_FunctionArguments_ReturnsSignatures: each created
// function yields its name and the types of its arguments, in order,
// folded to lower case and joined as PostgreSQL's identity signature lists
// them, whether the parameters are named or not and over several lines
// (MTIX-95.1.7).
func TestParseObjects_FunctionArguments_ReturnsSignatures(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []Function
	}{
		{"no arguments", "CREATE OR REPLACE FUNCTION f_none()\nRETURNS TRIGGER AS $$ BEGIN END; $$ LANGUAGE plpgsql;",
			[]Function{{Name: "f_none"}}},
		{"named parameters over several lines", "CREATE OR REPLACE FUNCTION f_named(\n    p_a TEXT,\n    p_b BIGINT)\n" +
			"RETURNS BOOLEAN AS $$ SELECT true $$ LANGUAGE sql;", []Function{{Name: "f_named", Args: "text, bigint"}}},
		{"unnamed parameters", "CREATE FUNCTION f_bare(text, Integer) RETURNS INT AS $$ SELECT 1 $$ LANGUAGE sql;",
			[]Function{{Name: "f_bare", Args: "text, integer"}}},
		{"a commented-out parameter is ignored", "CREATE FUNCTION f_c(\n  p_a text -- , p_b bigint\n) RETURNS INT AS $$ SELECT 1 $$ LANGUAGE sql;",
			[]Function{{Name: "f_c", Args: "text"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parseObjects([]string{tt.body}).signatures)
		})
	}
}
