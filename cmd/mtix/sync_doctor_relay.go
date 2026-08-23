// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/relay/retention"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/relay/tick"
)

// relayVerdictCodes is the FR-21 §9 registry, pinned.
//
// Doctor reports against exactly this list. A code that appears without
// being here is a spec change that did not happen, and a code here with
// no way to surface is a check nobody wrote — both are caught by the
// test that walks this against the packages that own each code.
var relayVerdictCodes = []string{
	"RELAY_SEGMENT_CORRUPT",
	"RELAY_GAP",
	"RELAY_SYMLINK",
	"RELAY_FOREIGN_ENTRY",
	"RELAY_PEER_ID_CONFLICT",
	"RELAY_KEY_ABSENT",
	"RELAY_KEY_PERMS",
	"RELAY_KEY_INVALID",
	"RELAY_KEY_SYMLINK",
	"RELAY_META_ABSENT",
	"RELAY_META_CORRUPT",
	"RELAY_META_SYMLINK",
	"RELAY_MODE_MISMATCH",
	"RELAY_HISTORY_DIVERGED",
	"RELAY_NO_SHARED_PROJECT",
	"RELAY_PUBLISHER_DIVERGED",
	"RELAY_AUTH_FAIL",
}

// cloudDriveMarkers are path fragments that indicate a third-party
// sync client owns the directory. A relay there ships the team's full
// plaintext event content to that provider — not a refusal, but not
// something to discover later either.
var cloudDriveMarkers = []string{
	"dropbox", "google drive", "googledrive", "onedrive",
	"icloud", "com~apple~clouddocs", "box sync", "sync.com", "pcloud",
}

// conflictedCopyMarkers name the artifacts cloud sync clients leave
// when two machines write one file. They fail the §5.2 segment grammar,
// so the reader already ignores them; doctor's job is to say they are
// there, because their presence means something is fighting over the
// directory.
var conflictedCopyMarkers = []string{
	"conflicted copy", "conflict copy", "-conflict", " (1)", "(case conflict",
}

// appendRelayChecks runs the FR-21 §9 doctor checks.
//
// Every failure names the command that clears it. A check that reports
// a problem without a next step makes an operator guess, and a guessing
// operator on a shared medium deletes things.
func appendRelayChecks(ctx context.Context, report DoctorReport) DoctorReport {
	dir := relayDir()
	if dir == "" {
		// Not configured is not unhealthy. A store that never wanted
		// this transport should not carry a red row forever.
		return appendCheck(report, "relay configured", true, "no relay configured (sync.relay.dir unset)")
	}

	ok, detail := checkRelayDirectory(dir)
	report = appendCheck(report, "relay reachable", ok, detail)
	if !ok {
		return report
	}

	doc, metaErr := metadata.Read(dir)
	if metaErr != nil {
		return appendCheck(report, "relay record", false, relayDoctorHint(metaErr))
	}
	report = appendCheck(report, "relay record", true,
		fmt.Sprintf("relay %s, %s", doc.RelayID, relayModeWord(doc.Authenticated)))

	for _, c := range []struct {
		name string
		run  func() (bool, string)
	}{
		{"relay key", func() (bool, string) { return checkRelayKey(doc) }},
		{"relay publishing", func() (bool, string) { return checkRelayPublishing(ctx) }},
		{"relay peers", func() (bool, string) { return checkRelayPeers(dir, doc) }},
		{"relay poll cadence", checkRelayPollCadence},
		{"relay snapshots", func() (bool, string) { return checkRelaySnapshots(dir) }},
		{"relay privacy", func() (bool, string) { return checkRelayPrivacy(dir, doc) }},
	} {
		ok, detail := c.run()
		report = appendCheck(report, c.name, ok, detail)
	}
	return report
}

// relayModeWord renders an authentication mode.
func relayModeWord(authenticated bool) string {
	if authenticated {
		return "authenticated"
	}
	return "UNAUTHENTICATED"
}

// checkRelayDirectory verifies the medium is reachable and free of the
// symlinks FR-21 §5.1 refuses.
func checkRelayDirectory(dir string) (bool, string) {
	info, err := os.Lstat(dir)
	if err != nil {
		return false, fmt.Sprintf("%s: %s\n    fix: mount the relay, or clear sync.relay.dir if this peer no longer uses it", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Sprintf("RELAY_SYMLINK: %s is a symlink\n    fix: point sync.relay.dir at the real directory", dir)
	}
	if _, _, err := segment.ListPeers(filepath.Join(dir, tick.PeersDirName)); err != nil {
		return false, relayDoctorHint(err)
	}
	return true, dir
}

// checkRelayKey verifies this peer holds the key it needs.
func checkRelayKey(doc *metadata.Relay) (bool, string) {
	if !doc.Authenticated {
		return true, "relay is unauthenticated (§8.3 opt-out); records carry no MAC"
	}
	epoch, ok := doc.CurrentKeyEpoch()
	if !ok {
		return false, "RELAY_META_CORRUPT: relay claims authentication but records no key epoch"
	}
	ring, err := keyring.Load(relayKeysDir())
	if err != nil {
		return false, relayDoctorHint(err)
	}
	if _, err := ring.For(epoch); err != nil {
		return false, relayDoctorHint(err)
	}
	return true, fmt.Sprintf("key epoch %d present, mode 0600", epoch)
}

// checkRelayPublishing surfaces the §5.7 refusal.
func checkRelayPublishing(ctx context.Context) (bool, string) {
	if app.store == nil {
		return false, "local store not initialized"
	}
	stack, err := openRelayStack()
	if err != nil {
		return false, relayDoctorHint(err)
	}
	if _, err := stack.Publisher.PublishPending(ctx); err != nil {
		return false, relayDoctorHint(err)
	}
	return true, "publishing"
}

// checkRelayPeers reports silence and outstanding retirement prompts.
func checkRelayPeers(dir string, doc *metadata.Relay) (bool, string) {
	ids, foreign, err := relayPeerDirs(dir)
	if err != nil {
		return false, relayDoctorHint(err)
	}
	retired := map[string]bool{}
	for _, p := range doc.RetiredPeers {
		retired[p] = true
	}
	var silent []string
	for _, s := range retention.SilentPeers(retention.SilenceInput{
		Peers:          ids,
		LastSeen:       relayLastSeen(dir, ids),
		Retired:        retired,
		SilentPeerDays: relayConfigInt("sync.relay.silent_peer_days", retention.DefaultSilentPeerDays),
		Now:            time.Now().UTC(),
	}) {
		if s.Silent {
			silent = append(silent, s.PeerID)
		}
	}
	var notes []string
	if len(foreign) > 0 {
		notes = append(notes, fmt.Sprintf("RELAY_FOREIGN_ENTRY: ignored %s", strings.Join(foreign, ", ")))
	}
	if len(silent) > 0 {
		return false, fmt.Sprintf("%d peer(s) silent past the threshold: %s\n"+
			"    fix: if they are gone for good, `mtix sync relay retire-peer <id>` releases the prune window;\n"+
			"         a peer that returns rejoins through `mtix sync relay clone`",
			len(silent), strings.Join(silent, ", "))
	}
	detail := fmt.Sprintf("%d peer(s), %d retired", len(ids), len(retired))
	if len(notes) > 0 {
		detail += "; " + strings.Join(notes, "; ")
	}
	return true, detail
}

// checkRelayPollCadence catches the configuration where every staleness
// check would cry wolf: a poll interval longer than the staleness
// threshold guarantees the peer looks stale between polls, so the
// warning stops carrying information.
func checkRelayPollCadence() (bool, string) {
	poll := relayConfigInt("sync.relay.poll_interval", 5)
	if poll == 0 {
		return true, "tick-only peer (poll_interval 0); converges on `mtix sync relay tick`"
	}
	silentDays := relayConfigInt("sync.relay.silent_peer_days", retention.DefaultSilentPeerDays)
	if poll > silentDays*86400 {
		return false, fmt.Sprintf(
			"poll_interval (%ds) exceeds the silence threshold (%dd): every check would cry wolf\n"+
				"    fix: lower sync.relay.poll_interval, or raise sync.relay.silent_peer_days",
			poll, silentDays)
	}
	return true, fmt.Sprintf("poll every %ds", poll)
}

// checkRelaySnapshots flags a bootstrap file left behind. It is a full
// plaintext copy of a store, so an old one is a privacy problem before
// it is a disk one.
func checkRelaySnapshots(dir string) (bool, string) {
	stale, err := bootstrap.StaleSnapshots(dir, bootstrap.StaleRequest{
		Now:           time.Now().UTC(),
		RetentionDays: relayConfigInt("sync.relay.retention_days", retention.DefaultRetentionDays),
	})
	if err != nil {
		return false, relayDoctorHint(err)
	}
	if len(stale) == 0 {
		return true, "no stale bootstrap snapshots"
	}
	names := make([]string, 0, len(stale))
	for _, s := range stale {
		names = append(names, fmt.Sprintf("%s (%dd)", s.Name, s.AgeDays))
	}
	return false, fmt.Sprintf(
		"%d stale bootstrap snapshot(s): %s\n"+
			"    a snapshot is a full plaintext copy of the store — remove it once every joiner has read past it\n"+
			"    fix: delete it from the relay's bootstrap directory once no peer still needs it",
		len(stale), strings.Join(names, ", "))
}

// checkRelayPrivacy advises on a relay under a third-party sync client,
// and flags the conflicted-copy artifacts such clients leave.
func checkRelayPrivacy(dir string, doc *metadata.Relay) (bool, string) {
	var notes []string
	lower := strings.ToLower(dir)
	for _, marker := range cloudDriveMarkers {
		if strings.Contains(lower, marker) {
			notes = append(notes,
				"relay path looks like a third-party sync folder: segments are MAC'd but NOT encrypted,\n"+
					"    so the team's full event content reaches that provider\n"+
					"    fix: move the relay to a volume you control, or accept it deliberately")
			break
		}
	}
	if artifacts := relayConflictedCopies(dir); len(artifacts) > 0 {
		notes = append(notes, fmt.Sprintf(
			"conflicted-copy artifacts in a peer directory: %s\n"+
				"    the reader ignores them (they fail the segment naming grammar) but their presence means\n"+
				"    two machines are writing one file\n"+
				"    fix: remove them and check that each peer publishes under its own id",
			strings.Join(artifacts, ", ")))
	}
	if !doc.Authenticated {
		notes = append(notes,
			"relay is UNAUTHENTICATED: anything that can write the directory can write events\n"+
				"    fix: re-init with authentication when the medium stops being fully trusted")
	}
	if len(notes) == 0 {
		return true, "no privacy advisories"
	}
	return false, strings.Join(notes, "\n  ")
}

// relayConflictedCopies finds cloud-sync duplicate artifacts under the
// peer directories. They are reported, never parsed and never removed.
func relayConflictedCopies(dir string) []string {
	peers, _, err := segment.ListPeers(filepath.Join(dir, tick.PeersDirName))
	if err != nil {
		return nil
	}
	var found []string
	for _, peer := range peers {
		segDir := filepath.Join(peer.Path, tick.SegmentsDirName)
		_, foreign, err := segment.ListSegments(segDir)
		if err != nil {
			continue
		}
		for _, name := range foreign {
			lower := strings.ToLower(name)
			for _, marker := range conflictedCopyMarkers {
				if strings.Contains(lower, marker) {
					found = append(found, filepath.Join(peer.PeerID, name))
					break
				}
			}
		}
	}
	return found
}

// relayDoctorHint renders a verdict with the command that clears it.
func relayDoctorHint(err error) string {
	code := relayCodeOf(err)
	if code == "" {
		return err.Error()
	}
	return fmt.Sprintf("%s\n    fix: %s", err.Error(), relayStallRecovery(code))
}

// relayCodeOf resolves a verdict to its FR-21 §9 registry code by
// asking each package that owns codes, in turn.
//
// The registry is one list and the codes live in several packages, so
// this is the single place they are reconciled — which is what lets a
// test assert that doctor covers exactly the pinned list and nothing
// else.
func relayCodeOf(err error) string {
	for _, resolve := range []func(error) string{
		segment.CodeOf, keyring.CodeOf, metadata.CodeOf, lifecycle.CodeOf,
	} {
		if code := resolve(err); code != "" {
			return code
		}
	}
	if publisher.CodeOf(err) != "" {
		return publisher.CodePublisherDiverged
	}
	return ""
}
