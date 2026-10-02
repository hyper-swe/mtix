// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package migrations

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// TruncateGuardFile is the migration that creates the append-only TRUNCATE
// guards (MTIX-95.1). `mtix sync harden` runs it to restore a missing guard.
const TruncateGuardFile = "016_append_only_truncate_guard.sql"

// Migration 017 has the hub stamp every sync_events row with its restore
// epoch and record restore collisions itself (MTIX-95.1.7). The transport
// calls the recorder by RecordCollisionSignature, and the doctor's schema
// check looks for these objects; a test pins them to what the file creates.
const (
	// RestoreEpochFile is migration 017.
	RestoreEpochFile = "017_hub_restore_epoch_stamp.sql"
	// RecordCollisionFunction is the name of the collision recorder.
	RecordCollisionFunction = "record_restore_collision"
	// RecordCollisionSignature is the recorder's identity signature.
	RecordCollisionSignature = "record_restore_collision(text, text, text, text, bigint)"
	// StampFunction is the function the stamp trigger executes.
	StampFunction = "hub_stamp_restore_epoch"
	// StampTrigger is the BEFORE INSERT trigger on sync_events.
	StampTrigger = "sync_events_stamp_restore_epoch"
)

// Function is one function the embedded migrations create: its name and
// the types of its arguments, lower case and separated by ", " as
// PostgreSQL lists an identity signature, or "" when it takes none
// (MTIX-95.1.7).
type Function struct {
	Name string
	Args string
}

// Signature returns the function's identity signature, name(args), the
// form to_regprocedure and GRANT, REVOKE and ALTER FUNCTION accept.
func (f Function) Signature() string {
	return f.Name + "(" + f.Args + ")"
}

// Trigger is one trigger the embedded migrations create: its name, the
// table it is on, the event that fires it (MTIX-95.1) and the function it
// executes (MTIX-95.7).
type Trigger struct {
	Name     string
	Table    string
	Event    string // INSERT, UPDATE, DELETE or TRUNCATE
	Function string
}

// Statement patterns the parser reads. Names are unquoted identifiers, as
// every migration writes them; PostgreSQL folds them to lower case.
var (
	lineComment   = regexp.MustCompile(`--[^\n]*`)
	createTable   = regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	serialColumn  = regexp.MustCompile(`(?i)\b([a-z_][a-z0-9_]*)\s+(?:smallserial|bigserial|serial[248]?)\b`)
	createFunc    = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+([a-z_][a-z0-9_]*)\s*\(([^)]*)\)`)
	createTrigger = regexp.MustCompile(`(?is)\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+([a-z_][a-z0-9_]*)\s+` +
		`(?:BEFORE|AFTER|INSTEAD\s+OF)\s+([a-z]+)\b.*?\bON\s+([a-z_][a-z0-9_]*)` +
		`.*?\bEXECUTE\s+(?:FUNCTION|PROCEDURE)\s+([a-z_][a-z0-9_]*)\s*\(`)
)

// objects is the parsed object set of the migrations.
type objects struct {
	tables     []string
	sequences  []string
	functions  []string
	signatures []Function
	triggers   []Trigger
}

// Tables returns the name of every table the embedded migrations create,
// sorted (MTIX-95.1). It is the sync table set: hub hardening and backup
// coverage read it instead of keeping their own list, so a new migration
// reaches both. A PG test pins it to the tables a migrated hub holds.
func Tables() ([]string, error) {
	o, err := embeddedObjects()
	if err != nil {
		return nil, err
	}
	return o.tables, nil
}

// Sequences returns the name of every sequence the serial columns of the
// embedded migrations' tables create (serial, serial2, serial4, serial8,
// smallserial or bigserial), <table>_<column>_seq as PostgreSQL names them,
// sorted (MTIX-95.1.4). A test fails when a migration creates a sequence
// any other way. A role that backs up the hub reads
// them with the sync tables. A PG test pins them to a migrated hub.
func Sequences() ([]string, error) {
	o, err := embeddedObjects()
	if err != nil {
		return nil, err
	}
	return o.sequences, nil
}

// Functions returns the name of every function the embedded migrations
// create, sorted (MTIX-95.1). FunctionSignatures gives their arguments.
func Functions() ([]string, error) {
	o, err := embeddedObjects()
	if err != nil {
		return nil, err
	}
	return o.functions, nil
}

// FunctionSignatures returns every function the embedded migrations
// create, with the types of its arguments, sorted by name (MTIX-95.1.7).
// Hub hardening and the doctor name each function by its signature. A PG
// test pins them to a migrated hub.
func FunctionSignatures() ([]Function, error) {
	o, err := embeddedObjects()
	if err != nil {
		return nil, err
	}
	return o.signatures, nil
}

// Triggers returns every trigger the embedded migrations create, sorted by
// table then name, including triggers created inside a DO block
// (MTIX-95.1), each with the function it executes (MTIX-95.7).
func Triggers() ([]Trigger, error) {
	o, err := embeddedObjects()
	if err != nil {
		return nil, err
	}
	return o.triggers, nil
}

// TruncateGuards returns the triggers that refuse TRUNCATE on the
// append-only tables, sorted by table (MTIX-95.1, FR-18.5).
func TruncateGuards() ([]Trigger, error) {
	all, err := Triggers()
	if err != nil {
		return nil, err
	}
	var guards []Trigger
	for _, tr := range all {
		if tr.Event == "TRUNCATE" {
			guards = append(guards, tr)
		}
	}
	return guards, nil
}

// embeddedObjects reads every embedded migration, in Files() order, and
// parses the objects they create.
func embeddedObjects() (objects, error) {
	files, err := Files()
	if err != nil {
		return objects{}, fmt.Errorf("list migrations: %w", err)
	}
	bodies := make([]string, 0, len(files))
	for _, f := range files {
		body, err := Read(f)
		if err != nil {
			return objects{}, fmt.Errorf("parse migrations: %w", err)
		}
		bodies = append(bodies, body)
	}
	return parseObjects(bodies), nil
}

// parseObjects collects the tables, the sequences of their serial columns,
// the functions with their argument types and the triggers created by the
// given migration bodies, ignoring -- comments. Each list is sorted and
// de-duplicated; names and types are lower-cased as PostgreSQL folds them.
func parseObjects(bodies []string) objects {
	tables := map[string]bool{}
	sequences := map[string]bool{}
	functions := map[string]string{} // name -> argument types
	triggers := map[Trigger]bool{}
	for _, body := range bodies {
		sql := lineComment.ReplaceAllString(body, "")
		for _, m := range createTable.FindAllStringSubmatchIndex(sql, -1) {
			table := strings.ToLower(sql[m[2]:m[3]])
			tables[table] = true
			for _, seq := range serialSequences(table, sql[m[1]:]) {
				sequences[seq] = true
			}
		}
		for _, m := range createFunc.FindAllStringSubmatch(sql, -1) {
			functions[strings.ToLower(m[1])] = argumentTypes(m[2])
		}
		for _, m := range createTrigger.FindAllStringSubmatch(sql, -1) {
			triggers[Trigger{
				Name:     strings.ToLower(m[1]),
				Event:    strings.ToUpper(m[2]),
				Table:    strings.ToLower(m[3]),
				Function: strings.ToLower(m[4]),
			}] = true
		}
	}
	names := map[string]bool{}
	for name := range functions {
		names[name] = true
	}
	o := objects{
		tables:    sortedKeys(tables),
		sequences: sortedKeys(sequences),
		functions: sortedKeys(names),
		triggers:  sortedTriggers(triggers),
	}
	for _, name := range o.functions {
		o.signatures = append(o.signatures, Function{Name: name, Args: functions[name]})
	}
	return o
}

// argumentTypes returns the types of a CREATE FUNCTION parameter list, in
// order, lower case and joined by ", ": the last word of each parameter,
// whether it is named ("p_a TEXT") or not ("text"), as the migrations
// write them (MTIX-95.1.7). A PG test pins the result to the catalog.
func argumentTypes(params string) string {
	var types []string
	for _, p := range strings.Split(params, ",") {
		if words := strings.Fields(p); len(words) > 0 {
			types = append(types, strings.ToLower(words[len(words)-1]))
		}
	}
	return strings.Join(types, ", ")
}

// serialSequences returns the sequences PostgreSQL creates for the serial
// columns of table, whose CREATE TABLE statement continues in rest up to
// its terminating semicolon (MTIX-95.1.4).
func serialSequences(table, rest string) []string {
	if end := strings.IndexByte(rest, ';'); end >= 0 {
		rest = rest[:end]
	}
	var out []string
	for _, m := range serialColumn.FindAllStringSubmatch(rest, -1) {
		out = append(out, table+"_"+strings.ToLower(m[1])+"_seq")
	}
	return out
}

// sortedKeys returns the keys of set in order, or nil when it is empty.
func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedTriggers returns the triggers of set ordered by table then name,
// or nil when it is empty.
func sortedTriggers(set map[Trigger]bool) []Trigger {
	if len(set) == 0 {
		return nil
	}
	out := make([]Trigger, 0, len(set))
	for tr := range set {
		out = append(out, tr)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Name < out[j].Name
	})
	return out
}
