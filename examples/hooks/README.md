# mtix git hooks (examples)

Reference implementations of git hooks and a GitHub Actions workflow that
keep `.mtix/tasks.json` in sync with the canonical Postgres store in
**BYO Postgres mode** (MTIX-14). Copy the one(s) you need into your repo
or your git server's hook directory.

> **Read first:** [`docs/SECURITY-MODEL.md`](../../docs/SECURITY-MODEL.md).
> The hooks below are *security-relevant*. The trust model in that document
> tells you which one is appropriate for your environment.

---

## What's in here

| File | Where it runs | Purpose | Bypassable? |
|---|---|---|---|
| `pre-push` | Each developer's machine | Refresh and commit `.mtix/tasks.json` snapshot before push | Yes (`git push --no-verify`) |
| `pre-receive` | Self-hosted git server | Reject pushes whose snapshot disagrees with PG | No (server enforced) |
| `github-action.yml` | GitHub.com runner | Same as `pre-receive`, but for GitHub-hosted repos | No, when paired with branch protection |

---

## Trade-off 1: client-side vs server-side enforcement

These hooks form a continuum, not alternatives:

* **Client-side only (`pre-push`)** — convenience layer. Catches honest
  mistakes ("I forgot to regenerate the snapshot") without any server
  infrastructure. Good for a small trusted team where everyone is
  cooperating in good faith. *Anyone with `--no-verify` can bypass it.*
  This is the right default for solo and small-team workflows.

* **Server-side (`pre-receive` or `github-action.yml`)** — enforcement
  gate. Required when the audit trail must be airtight (regulated
  environments, post-mortem requirements, anything where "I forgot the
  hook" is not an acceptable answer). Costs more to operate (DSN on
  the runner, network reachability to PG).

Most teams should install **both**: `pre-push` locally for fast feedback,
`pre-receive` (or the GH Action) on the server for the hard guarantee.
That's the pattern documented in the safety-critical workflow guide.

---

## Trade-off 2: separate commit vs amend (`pre-push` only)

When `pre-push` detects drift in `.mtix/tasks.json`, it has two ways to
record the regenerated snapshot:

* **Default — separate `chore(snapshot)` commit.** The developer's
  commit is theirs; the snapshot commit is the tool's. Audit-friendly,
  cannot accidentally rewrite a signed commit, cannot mutate a commit
  that was already pushed elsewhere. The downside is a slightly noisier
  history.

* **Opt-in — amend (`MTIX_HOOK_AMEND=1`).** Folds the snapshot into the
  developer's last commit. Cleaner history. The downsides are real: it
  re-applies signatures, drops co-author trailers, and rewrites a
  commit you're about to push, which can confuse anyone who has the
  pre-amend SHA in another worktree.

Pick one and document the choice in your contributing guide. The hook
defaults to the safer option (separate commit).

---

## Trade-off 3: warn-and-skip vs hard-fail on PG outage (`pre-push` only)

`pre-push` defaults to **warn-and-skip**: if `mtix snapshot` fails
because PG is unreachable, the hook prints a warning to stderr and
exits 0, allowing the push to proceed.

This is deliberate. mtix is a primitive, not a workflow opinion. The
ability to push your code should not be coupled to the availability of
your task store. The next person who pushes after PG comes back up
will regenerate the snapshot and pick up the drift.

If your team needs hard-fail behaviour locally, wrap the hook in a
shell snippet that re-exits non-zero, or rely on `pre-receive` /
`github-action.yml` for enforcement (those *do* hard-fail).

---

## Installing each hook

### `pre-push` (client-side)

```bash
# From the project root:
cp examples/hooks/pre-push .git/hooks/pre-push
chmod +x .git/hooks/pre-push
```

Override defaults via environment variables (set in your shell rc, in
`direnv`, or in CI):

| Variable | Default | Purpose |
|---|---|---|
| `MTIX_BIN` | `$(command -v mtix)` | Pin to an absolute path; skips PATH search at run time |
| `MTIX_HOOK_AMEND` | unset | Set to `1` to amend instead of separate commit |
| `MTIX_HOOK_TIMEOUT` | `30s` | Snapshot timeout; passed to `mtix snapshot --timeout` |

### `pre-receive` (self-hosted git server)

On the git server, in the bare repo directory:

```bash
cp /path/to/mtix/examples/hooks/pre-receive hooks/pre-receive
chmod +x hooks/pre-receive
```

Required environment on the git server:

* `mtix` CLI installed and on `PATH` (or `MTIX_BIN` set).
* `MTIX_PG_DSN` exported, with a **read-only** PG role.

Restart the git server (or its post-receive supervisor) so the new
environment is picked up.

### `github-action.yml`

```bash
mkdir -p .github/workflows
cp examples/hooks/github-action.yml .github/workflows/mtix-snapshot.yml
```

Add the secret in your GitHub repo:

* Settings → Secrets and variables → Actions → New repository secret
* Name: `MTIX_PG_DSN`
* Value: a read-only DSN to your task store

Pair with a branch protection rule that *requires* the
`mtix-snapshot-freshness / verify-snapshot` check to pass on the
target branch.

---

## Security caveats

* **Client-side hooks are bypassable.** `git push --no-verify` skips
  every client hook. If you require enforcement, the only honest option
  is server-side (`pre-receive` or branch-protected GH Action).
* **`MTIX_PG_DSN` is a credential.** Treat it like a secret: never put
  it in tracked config, only in env vars or `.mtix/secrets` (gitignored,
  mode `0600`). The GitHub Action uses Actions secrets; the
  `pre-receive` server-side role should be read-only.
* **Hooks must use `MTIX_BIN`, not bare `mtix`.** A poisoned working
  directory can shadow `mtix` via a malicious `./mtix` if PATH includes
  `.`. The hooks here resolve `mtix` once at the top via `command -v`
  and then use the absolute path everywhere; the regression test
  `TestExampleHooks_PrePush_AbsolutePathToMtix` guards this.
* **No task content is echoed.** Task titles and prompts can contain
  shell metacharacters and ANSI escapes. The hooks only print file
  paths and counts.
* **Snapshot is bounded.** All snapshot calls pass `--timeout`. A hung
  PG cannot stall your push beyond that window.

For the full trust model (who is trusted, what BYO PG protects against,
what it does *not* protect against), see
[`docs/SECURITY-MODEL.md`](../../docs/SECURITY-MODEL.md).

---

## Testing the hooks locally

The repo's Go test suite includes regression tests for these hooks under
the `mtix` package (file `example_hooks_test.go`):

```bash
go test -run TestExampleHooks ./...
```

Tests covered:

* `TestExampleHooks_PrePush_ShellLinted` — `shellcheck` clean (skipped if shellcheck is not installed)
* `TestExampleHooks_PreReceive_ShellLinted`
* `TestExampleHooks_PrePush_NoUnquotedVariables` — every `$VAR` is double-quoted
* `TestExampleHooks_PrePush_AbsolutePathToMtix` — `MTIX_BIN` discipline
* `TestExampleHooks_PrePush_TimeoutFlagPassed` — `--timeout` is always passed to `mtix snapshot`
* `TestExampleHooks_GithubAction_YAMLValid` — structural sanity for the workflow file
* `TestExampleHooks_PrePush_RunsAgainstFakeRepo` — integration test using a stub `mtix` binary

To run the integration test you need `git` and `bash` on your PATH. To
run the lint test you need `shellcheck` (`brew install shellcheck`).

---

## See also

* [`docs/SECURITY-MODEL.md`](../../docs/SECURITY-MODEL.md) — trust and threat model
* [`docs/WORKFLOWS.md`](../../docs/WORKFLOWS.md) — solo / small-team / safety-critical setups
* MTIX-14 (BYO Postgres epic) and MTIX-14.5 (this directory) for design rationale
