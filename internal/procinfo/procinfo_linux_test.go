// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package procinfo

import "testing"

func TestParseStatStart_CommandWithParenthesesAndSpaces_ReadsField22(t *testing.T) {
	stat := []byte("42 (a b) c) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 98765 23 24")
	got, err := parseStatStart(stat)
	if err != nil || got != "98765" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parseStatStart([]byte("42 (x) S 1")); err == nil {
		t.Fatal("short stat must fail")
	}
	if _, err := parseStatStart([]byte("garbage")); err == nil {
		t.Fatal("no parenthesis must fail")
	}
}

func TestParseStatInfo_ReadsParentNameAndStart(t *testing.T) {
	p, err := parseStatInfo([]byte("42 (a b) c) S 7 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 98765 23 24"))
	if err != nil || p.PPID != 7 || p.Name != "a b) c" || p.StartTime != "98765" {
		t.Fatalf("got %+v, %v", p, err)
	}
	if _, err := parseStatInfo([]byte("42 (x) S 1")); err == nil {
		t.Fatal("short stat must fail")
	}
	if _, err := parseStatInfo([]byte("42 (x) S zz 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 98765 23")); err == nil {
		t.Fatal("bad ppid must fail")
	}
}

func TestSplitCmdline_SplitsOnNULAndKeepsEmptyUnknown(t *testing.T) {
	got := splitCmdline([]byte("mtix\x00-C\x00/p\x00daemon\x00"))
	if len(got) != 4 || got[0] != "mtix" || got[3] != "daemon" {
		t.Fatalf("got %q", got)
	}
	if splitCmdline(nil) != nil || splitCmdline([]byte{0}) != nil {
		t.Fatal("an empty cmdline means unknown arguments")
	}
}
