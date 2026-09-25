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
	createFunc    = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+([a-z_][a-z0-9_]*)\s*\(`)
	createTrigger = regexp.MustCompile(`(?is)\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+([a-z_][a-z0-9_]*)\s+` +
		`(?:BEFORE|AFTER|INSTEAD\s+OF)\s+([a-z]+)\b.*?\bON\s+([a-z_][a-z0-9_]*)` +
		`.*?\bEXECUTE\s+(?:FUNCTION|PROCEDURE)\s+([a-z_][a-z0-9_]*)\s*\(`)
)

// objects is the parsed object set of the migrations.
type objects struct {
	tables    []string
	functions []string
	triggers  []Trigger
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

// Functions returns the name of every function the embedded migrations
// create, sorted (MTIX-95.1). Each takes no arguments.
func Functions() ([]string, error) {
	o, err := embeddedObjects()
	if err != nil {
		return nil, err
	}
	return o.functions, nil
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

// parseObjects collects the tables, functions and triggers created by the
// given migration bodies, ignoring -- comments. Each list is sorted and
// de-duplicated; names are lower-cased as PostgreSQL folds them.
func parseObjects(bodies []string) objects {
	tables := map[string]bool{}
	functions := map[string]bool{}
	triggers := map[Trigger]bool{}
	for _, body := range bodies {
		sql := lineComment.ReplaceAllString(body, "")
		for _, m := range createTable.FindAllStringSubmatch(sql, -1) {
			tables[strings.ToLower(m[1])] = true
		}
		for _, m := range createFunc.FindAllStringSubmatch(sql, -1) {
			functions[strings.ToLower(m[1])] = true
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
	return objects{
		tables:    sortedKeys(tables),
		functions: sortedKeys(functions),
		triggers:  sortedTriggers(triggers),
	}
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
