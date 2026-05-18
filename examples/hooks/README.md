# mtix git hooks (examples)

Reference implementations of git hooks and a GitHub Actions workflow
that complement mtix's **sync mode** (FR-18 / MTIX-15). Copy the one(s)
you need into your repo or your git server's hook directory.

> **Read first:** [`docs/SECURITY-MODEL.md`](../../docs/SECURITY-MODEL.md).
> The hooks below are *security-relevant*. The trust model in that
> document tells you which one is appropriate for your environment.

---

## What's in here (v0.2.0-beta status)

| File | Where it runs | Purpose | Status in v0.2.0-beta |
|---|---|---|---|
| `pre-push` | Each developer's machine | Drains the local `sync_events` queue to the hub via `mtix sync push` before `git push`; refreshes `.mtix/tasks.json` if the local SQLite is ahead. | **Active.** Bypassable with `git push --no-verify`. |
| `pre-receive` | Self-hosted git server | Was: regenerate snapshot from PG and reject drift. | **Deferred to v0.2.1.** Ships as a no-op stub. The v0.1.x model assumed PG was the canonical store; v0.2.0-beta's FR-18 sync model has SQLite canonical and PG as a replication hub, so the server-side validation needs a redesign. |
| `github-action.yml` | GitHub.com runner | Was: same role as `pre-receive`, GitHub-hosted. | **Deferred to v0.2.1.** Ships as a no-op informational workflow. |

---

## What works today (v0.2.0-beta)

### Client-side: install `pre-push`

```bash
cp examples/hooks/pre-push .git/hooks/pre-push
chmod +x .git/hooks/pre-push
```

The hook calls `mtix sync push` with `MTIX_SYNC_HOOK=1` so transient
hub errors (connection refused, TLS handshake timeout) are warn-and-skip
rather than blocking `git push`. Schema mismatch and authentication
errors still fail the push — those are operator misconfigurations, not
transient.

After a successful push, the hook also refreshes `.mtix/tasks.json` via
`mtix sync --fix` and commits the diff under
`chore(snapshot): tasks.json refresh @ <timestamp>` so the git history
shows what the hub saw at that point.

**Required environment:**
- `mtix` on PATH (or `MTIX_BIN` set to an absolute path).
- `MTIX_SYNC_DSN` set OR `.mtix/secrets` exists (mode 0600). Sync is
  opt-in — if no DSN is configured the hook exits silently.

**Optional:**
- `MTIX_HOOK_AMEND=1` to fold the snapshot into the developer's last
  commit instead of a separate `chore(snapshot)` commit.

### Daemon-as-service (recommended for durability)

For compliance-grade durability across machine loss, run the daemon
on every developer's machine as a systemd / launchd service. The
daemon prints the unit file for you:

```bash
# Linux
mtix sync daemon --install | sudo tee /etc/systemd/system/mtix-sync.service
sudo systemctl enable --now mtix-sync

# macOS — launchd plist is also emitted by --install (operator places it)
mtix sync daemon --install
```

The daemon polls `mtix sync pull` every 30 seconds (configurable via
`--interval SEC`). Pair with the pre-push hook (which handles push)
for inbound + outbound auto-sync.

### CI hub-health gate

Even without server-side snapshot enforcement, you can gate merges on
hub reachability via a CI job that runs `mtix sync doctor --json`. The
doctor command exits 2 if any of its 5 health checks fail (PG
reachable, schema current, queue draining, no orphan applied, secrets
file mode). Add it as a required check in your branch protection rule.

```yaml
# .github/workflows/mtix-hub-health.yml
name: mtix-hub-health
on: [pull_request]
jobs:
  doctor:
    runs-on: ubuntu-latest
    env:
      MTIX_SYNC_DSN: ${{ secrets.MTIX_SYNC_DSN }}  # read-only role
    steps:
      - uses: actions/checkout@v4
      - run: go install github.com/hyper-swe/mtix/cmd/mtix@latest
      - run: mtix sync doctor --json
```

---

## What's coming in v0.2.1

Server-side enforcement of pushed-state freshness in the FR-18 model.
Likely shape: a hook (and equivalent CI workflow) that connects to the
hub, derives the canonical state, and rejects pushes whose
`.mtix/tasks.json` does not match. The challenge is that the canonical
state in v0.2.0-beta is per-CLI local SQLite — the server has no
SQLite of its own, only the hub. Design tracked under MTIX-15 follow-ups.

If your safety profile cannot wait, the workarounds above (daemon +
`pre-push` + CI doctor) cover the same ground at lower confidence. Reach
out via GitHub Issues if you need server-side enforcement before v0.2.1.

---

## Test references

All hooks ship with regression tests in `example_hooks_test.go`:

* `TestExampleHooks_PrePush_*` — covers the active `pre-push` hook end
  to end (fake repo, stub `mtix`, with and without DSN configured).
* `TestExampleHooks_PreReceive_*` — covers the stub (existence,
  executability, shell hygiene). The integration test was removed when
  the hook became a no-op; it will return in v0.2.1.
* `TestExampleHooks_GithubAction_YAMLValid` — covers the workflow's
  YAML shape.

---

## See also

* [`docs/SECURITY-MODEL.md`](../../docs/SECURITY-MODEL.md) — full trust
  and threat model (v1.1; covers the FR-18 sync hub trust boundary).
* [`docs/SYNC-DESIGN.md`](../../docs/SYNC-DESIGN.md) — architectural
  overview of the event-sourced sync layer.
* [`docs/SYNC-PROTOCOL.md`](../../docs/SYNC-PROTOCOL.md) — protocol-level
  details for contributors and auditors.
* [`CHANGELOG.md`](../../CHANGELOG.md) — v0.2.0-beta entry covers the
  full release and the upgrade path for v0.1.x users.
