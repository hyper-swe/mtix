# Release Checklist

**Run top to bottom before pushing any release tag.** Every box is either
checked or has a written reason it does not apply. Copy this list into the
release notes draft (or a scratch file committed with the release prep)
and fill it in — the artifact of a completed run is part of the release.

Created for MTIX-22 after two v0.2.0-beta failure modes: a deferred audit
finding that was never re-reviewed caused three tag re-issues, and a new
MCP tool shipped undocumented (caught by a user, MTIX-18).

---

## 1. Deferred-finding review *(the v0.2.0 tag-re-issue failure mode)*

- [ ] List every audit/review finding deferred since the last tag
      (search ticket notes and `docs/audit/` for "deferred", plus any
      findings parked in session memory or review docs).
- [ ] For each: apply the fix now, or write a disposition (why it is
      safe to ship, and what would trigger revisiting). **No finding
      crosses a release boundary silently.**
- [ ] Record the dispositions in `docs/audit/` (append to the relevant
      audit doc or add a `<release>-dispositions` note).

## 2. Docs-coverage sweep *(the undocumented-tool failure mode)*

Diff the surface area since the last tag and verify each item is documented:

- [ ] New/changed **commands and flags** → USERMANUAL + `docs/CLI_REFERENCE.md`
      (`git diff <last-tag>..HEAD --stat -- cmd/mtix/` as the source of truth).
- [ ] New **MCP tools** → `docs/MCP-SETUP.md` and the USERMANUAL tool list
      (compare `mtix mcp` registered tools against the docs; this is the
      exact MTIX-18 miss).
- [ ] New **environment knobs / config keys** → USERMANUAL.
- [ ] New **exit codes or output contracts** → USERMANUAL.
- [ ] **CHANGELOG**: `[Unreleased]` content moved into a dated release
      section; headline written; migration notes if the schema version moved.

## 3. Quality gates (automated)

- [ ] `make preflight` — green scorecard.
- [ ] `make verify` — race suite, web tests, lint, coverage report, build.
- [ ] Fault-injection conformance (NFR-2.8):
      `MTIX_FAULTFS_DIR=$(scripts/faultfs.sh create) go test ./e2e/faultinject/ -tags=faultinject -count=1`
      (CI runs this on every push; run locally only if CI is not green on HEAD).
- [ ] Traceability gate: `go test . -run TestTraceability -count=1`
      (every QUALITY-STANDARDS §3.6 scenario maps to existing tests).
- [ ] `make security-audit` — govulncheck + npm audit clean, release
      artifacts updated.
- [ ] CI fully green on the exact commit to be tagged (all jobs,
      including `test-fault-injection` and the postgres contract suite).

## 4. Tag and release

- [ ] Version bump consistent everywhere it appears (ldflags are
      build-time; check docs/examples that mention versions).
- [ ] Tag the exact CI-green commit; never re-issue a tag — if anything
      is wrong, fix forward and bump.
- [ ] Release workflow green end to end (goreleaser artifacts, cloud-PG
      tag suites, binary DSN-string sweep).
- [ ] Post-release smoke: install the published artifact (brew/binary)
      in a clean directory; `mtix init`, `mtix create`, `mtix list`,
      `mtix recover` help, `mtix mcp` handshake.

## 5. Announce and follow up

- [ ] Release notes mention anything users must act on (recovery
      runbook changes, new defaults like automatic backups).
- [ ] Notify any user whose reported issue this release fixes.
- [ ] File tickets for anything this checklist surfaced but did not block on.
