// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// handleCLIRaw builds the retained raw event for id; the id is JSON-encoded, so
// NUL becomes \u0000 and invalid bytes become U+FFFD, as for a pulled event.
func handleCLIRaw(t *testing.T, id string) string {
	t.Helper()
	encoded, err := json.Marshal(id)
	require.NoError(t, err)
	return `{"project_prefix":"TEST","author_id":"peer","author_machine_hash":"0123456789abcdef","vector_clock":{},"lamport_clock":1,"event_id":` + string(encoded) + `,"node_id":"TEST-1é","op_type":"create_node","payload":{"title":"held"}}`
}

// splitShellWords parses a command line the way a POSIX shell would for the
// subset mtix prints: single-quoted words, '\” joins and plain words.
func splitShellWords(t *testing.T, line string) []string {
	t.Helper()
	var words []string
	var cur strings.Builder
	inWord, inQuote := false, false
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case inQuote:
			if r == '\'' {
				inQuote = false
			} else {
				cur.WriteRune(r)
			}
		case r == '\'':
			inQuote, inWord = true, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case r == '\\' && i+1 < len(runes) && runes[i+1] == '\'':
			cur.WriteRune('\'')
			inWord = true
			i++
		case r == '\\':
			t.Fatalf("backslash outside quotes is not typeable here: %q", line)
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	require.False(t, inQuote, "unterminated quote in %q", line)
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

func ackByHandle(t *testing.T, id string) string {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, runSyncQuarantineAcknowledgeHandle(t.Context(), &out, service.QuarantineHandle(id), "reviewed"))
	return out.String()
}

func TestQuarantineAck_NULEventID_ResolvesByHandle(t *testing.T) {
	initAckCLI(t)
	id := "evt-\x00-nul"
	seedAckCLI(t, id, "pull", handleCLIRaw(t, id), "invalid node id")
	assert.NotContains(t, ackByHandle(t, id), "\x00")
	events, err := app.syncSvc.ListQuarantined(t.Context())
	require.NoError(t, err)
	acked := 0
	for _, q := range events {
		if q.EventID == id {
			require.NotNil(t, q.Acknowledgement)
			assert.Equal(t, id, q.Acknowledgement.EventID, "receipt keyed by the real id bytes")
			acked++
		}
	}
	require.Equal(t, 1, acked)
}

func TestQuarantineAck_InvalidUTF8ID_ResolvesByHandle(t *testing.T) {
	initAckCLI(t)
	id := "evt-\xff\xfe-bad"
	seedAckCLI(t, id, "pull", handleCLIRaw(t, id), "invalid node id")
	out := ackByHandle(t, id)
	assert.Contains(t, out, "acknowledged")
	again := ackByHandle(t, id)
	assert.Contains(t, again, "already acknowledged", "repeat returns the first receipt")
}

func TestQuarantineAck_HandleFlag_ExclusiveWithPositionalID(t *testing.T) {
	initAckCLI(t)
	other := "0193fb00-0000-7000-8000-0000000000aa"
	seedAckCLI(t, other, "pull", handleCLIRaw(t, other), "invalid node id")
	// The handle resolves (it names the second, eligible event), so only the
	// argument-count refusal can stop the call.
	_, err := executeCmd(newRootCmd(), "sync", "quarantine", "acknowledge", ackCLIEventID, "--handle", service.QuarantineHandle(other), "--reason", "r")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acknowledge", "the refusal names the command")
	_, err = executeCmd(newRootCmd(), "sync", "quarantine", "acknowledge", "--reason", "r")
	require.Error(t, err, "neither an id nor a handle")
	assert.Contains(t, err.Error(), "requires 1 argument")
	events, listErr := app.syncSvc.ListQuarantined(t.Context())
	require.NoError(t, listErr)
	require.Len(t, events, 2)
	for _, q := range events {
		assert.Nil(t, q.Acknowledgement, "a refused call writes no receipt for %s", q.EventID)
	}
}

func TestQuarantineAck_CobraHandleWithoutPositional_WritesTheReceipt(t *testing.T) {
	initAckCLI(t)
	id := "evt-\x00-cobra"
	seedAckCLI(t, id, "pull", handleCLIRaw(t, id), "invalid node id")
	cmd := newSyncQuarantineCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"acknowledge", "--handle", service.QuarantineHandle(id), "--reason", "reviewed"})
	require.NoError(t, cmd.ExecuteContext(t.Context()))
	assert.Contains(t, out.String(), "acknowledged")
	events, err := app.syncSvc.ListQuarantined(t.Context())
	require.NoError(t, err)
	for _, q := range events {
		switch q.EventID {
		case id:
			require.NotNil(t, q.Acknowledgement)
			assert.Equal(t, "reviewed", q.Acknowledgement.Reason)
		case ackCLIEventID:
			assert.Nil(t, q.Acknowledgement, "only the named event is acknowledged")
		}
	}
}

func TestQuarantineAck_ReplacementRuneForAValidStoredRune_IsRefused(t *testing.T) {
	initAckCLI(t)
	id := "evt-é-x"
	raw := strings.Replace(handleCLIRaw(t, id), "é", "�", 1)
	require.NotEqual(t, handleCLIRaw(t, id), raw)
	seedAckCLI(t, id, "pull", raw, "invalid node id")
	err := runSyncQuarantineAcknowledge(t.Context(), &bytes.Buffer{}, id, "reviewed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}

func TestQuarantineAck_UnknownHandle_RefusedWithoutWriting(t *testing.T) {
	initAckCLI(t)
	var out bytes.Buffer
	err := runSyncQuarantineAcknowledgeHandle(t.Context(), &out, "deadbeefdead", "reviewed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handle")
	events, listErr := app.syncSvc.ListQuarantined(t.Context())
	require.NoError(t, listErr)
	require.Len(t, events, 1)
	assert.Nil(t, events[0].Acknowledgement)
}

func TestQuarantineList_HostileIDs_PrintHandleTextAndJSON(t *testing.T) {
	initAckCLI(t)
	want := map[string]string{}
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 4096 || f.Value == ackCLIEventID {
			continue
		}
		seedAckCLI(t, f.Value, "pull", handleCLIRaw(t, f.Value), "invalid node id")
		want[service.QuarantineHandle(f.Value)] = f.Name
	}
	var text bytes.Buffer
	require.NoError(t, runSyncQuarantineList(t.Context(), &text))
	assert.Contains(t, text.String(), "HANDLE")
	for h, name := range want {
		assert.Contains(t, text.String(), h, name)
	}
	for _, r := range text.String() {
		if r < 0x20 && r != '\n' && r != '\t' || r >= 0x7f && r <= 0x9f {
			t.Fatalf("list output holds control rune %U", r)
		}
	}
	app.jsonOutput = true
	var js bytes.Buffer
	require.NoError(t, runSyncQuarantineList(t.Context(), &js))
	var rows []struct {
		EventID string `json:"event_id"`
		Handle  string `json:"handle"`
		SHA256  string `json:"event_id_sha256"`
	}
	require.NoError(t, json.Unmarshal(js.Bytes(), &rows))
	got := map[string]bool{}
	for _, r := range rows {
		assert.Len(t, r.Handle, service.QuarantineHandleLen)
		assert.Equal(t, r.Handle, r.SHA256[:service.QuarantineHandleLen])
		got[r.Handle] = true
	}
	for h, name := range want {
		assert.True(t, got[h], name)
	}
}

func TestDoctorFixLines_AllHostileIDs_TypeableAndResolve(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 4096 {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			initAckCLI(t)
			seedAckCLI(t, f.Value, "pull", handleCLIRaw(t, f.Value), "invalid node id")
			for _, all := range [][]string{{f.Value}, {f.Value, ackCLIEventID}} {
				fix := quarantineAcknowledgementCommand(f.Value, 1, all)
				require.NotEmpty(t, fix)
				for _, r := range fix {
					require.True(t, r >= 0x20 && r < 0x7f, "fix line must be printable ASCII, got %U in %q", r, fix)
				}
				tail := fix[strings.Index(fix, ", then: ")+len(", then: "):]
				words := splitShellWords(t, tail)
				require.Equal(t, []string{"mtix", "sync", "quarantine", "acknowledge", "--handle"}, words[:5], fix)
				require.Len(t, words, 6, "no reason value: copying it unchanged must be refused")
				var handle string
				for i, w := range words {
					if w == "--handle" && i+1 < len(words) {
						handle = words[i+1]
					}
				}
				require.NotEmpty(t, handle)
				var out bytes.Buffer
				require.NoError(t, runSyncQuarantineAcknowledgeHandle(t.Context(), &out, handle, "reviewed"), fix)
				events, err := app.syncSvc.ListQuarantined(t.Context())
				require.NoError(t, err)
				found := false
				for _, q := range events {
					if q.EventID == f.Value && q.Acknowledgement != nil {
						found = true
					}
				}
				require.True(t, found, "handle in %q must resolve to the event", fix)
			}
		})
	}
}

func TestPrintedHints_ShellMetacharIDs_Quoted(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		t.Run(f.Name, func(t *testing.T) {
			word, ok := format.ShellWord(f.Value)
			if !ok {
				assert.Contains(t, stillBlockedLine(f.Value), "run mtix dep show with this id", "no command for an id that cannot be typed")
				assert.NotContains(t, stillBlockedLine(f.Value), "`mtix dep show")
				return
			}
			require.True(t, ok)
			for _, r := range word {
				require.True(t, unicode.IsPrint(r), "word holds non-printable %U", r)
			}
			assert.Equal(t, []string{f.Value}, splitShellWords(t, word), "quoted word parses back to the exact id")
			hint := stillBlockedLine(f.Value)
			require.Contains(t, hint, "mtix dep show ")
			tail := hint[strings.Index(hint, "mtix dep show "):]
			tail = strings.TrimSuffix(tail, "`)\n")
			want := []string{"mtix", "dep", "show", f.Value}
			if f.Value[0] == '-' {
				want = []string{"mtix", "dep", "show", "--", f.Value}
			}
			assert.Equal(t, want, splitShellWords(t, tail))
		})
	}
}
