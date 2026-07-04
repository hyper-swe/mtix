# FR-19 — Event Hooks & Agent Notifications

> Status: proposed 2026-07-04. Author: HyperSWE frontier orchestrator
> (Fable 5), from two days of live two-agent operation on the HumanPseudo
> project. Requirement source: the human is currently the message bus.
>
> Refined 2026-07-04 (architecture review): the inbox is a QUERY over the
> durable sync journal, not a separate delivery pipeline (FR-19.4); the exec
> adapter is off by default and gated by a LOCAL, content-hash trust of
> hooks.yaml (§3 Security). Supersedes MTIX-6.2 (outgoing webhooks); MTIX-6.1
> (`--changed-since`) is complementary and stays separate.

## 1. Motivation

In multi-agent operation (frontier orchestrator + worker agents), every
cross-agent handoff today needs a human relay: worker finishes → human
tells frontier; frontier rules → human tells worker. mtix already holds
the state (nodes, comments, status) and the events (journal, `/ws/events`)
— but agents built on request-driven LLM harnesses cannot subscribe to a
WebSocket. The missing piece is ADDRESSED, DURABLE, WAKE-CAPABLE delivery:
"when something addressed to me happens, I find out without polling, even
if I was not running when it happened."

Observed concrete flows this FR must automate (real, from the field):
- worker marks HP-1.7.1 done → frontier should be notified (review/rule)
- frontier posts a ruling comment on HP-1.7.2 → worker should wake and act
- worker files a `defect:` node owned by `frontier-model` → frontier notified
- any status change on a node an agent is `--watch`ing → that agent notified

## 2. Functional requirements

### FR-19.1 Addressed comments
`mtix comment <id> "text" --to <agent>` sets an addressee on the
annotation. Additionally, `@<agent-name>` tokens in comment text are
parsed as addressees. Addressees generate `comment.addressed` events.

### FR-19.2 Subscriptions (hooks.yaml)
Per-project config `.mtix/hooks.yaml`, human-committed:

    hooks:
      - name: wake-worker-on-ruling
        match:
          events: [comment.addressed, status.changed, node.created]
          to-agent: opus-4.8          # optional
          from-agent-not: opus-4.8    # loop guard (see FR-19.6)
          under: HP-1                 # optional subtree filter
          status-to: [done, blocked]  # optional, for status.changed
        deliver: [inbox, exec]        # one or more adapters
        exec:
          command: ["/usr/local/bin/wake-worker.sh"]   # argv, NO shell
          timeout-seconds: 30

Filters compose with AND; a hook with only `events:` matches broadly.

### FR-19.3 Delivery adapters
1. **inbox** (default, always available): append the event to the
   addressee's durable inbox (see FR-19.4).
2. **exec**: run the configured argv with the event provided as
   `MTIX_EVENT` env var (JSON) and on stdin. No shell interpolation of
   event content, ever. Timeout enforced; output captured to hook log.
3. **webhook**: POST the event JSON to a configured URL (localhost or
   remote); retries with backoff, 3 attempts; failures logged, never block
   the originating mutation. (Subsumes the earlier MTIX-6.2 "outgoing
   webhooks" ticket, now folded into this FR.)
4. **append-file**: append a human/agent-readable line + JSON to a
   configured file path inside the project (e.g. `FRONTIER-INBOX.md`) —
   the zero-infrastructure adapter for file-reading agents.

Hook execution is ASYNC after the mutation commits; a failing hook must
never fail or delay the mutation itself.

### FR-19.4 Durable per-agent inbox (a query over the journal, not a mailbox)
The inbox is DERIVED, not delivered: it is a query over mtix's durable,
sequence-ordered event journal (the same `sync_events`-backed log the sync
layer already replicates) — "journaled events addressed to `<agent>` with
sequence > that agent's ack cursor." Nothing is copied into a separate mailbox,
so durability, dedupe, restart-survival, and team-sync all fall out by
construction: the journal is already durable + synced, and the ONLY new state
is a per-agent **ack cursor** (the highest journal sequence that agent has
acked). NOTE: the existing in-memory `service/broadcaster` Hub + `/ws/events`
is live pub/sub only (it drops slow subscribers) and is NOT the inbox
substrate — the durable journal is.

- `mtix inbox --agent <name> [--json]` — list events addressed to `<name>`
  past its ack cursor, oldest first.
- `mtix inbox --agent <name> --wait [--timeout <sec>]` — long-poll: return
  immediately if the query is non-empty, else block until a new matching event
  is journaled or timeout (exit 0 with events / exit 3 on timeout, empty).
  This is the primitive a worker's outer loop parks on between tasks.
- `mtix inbox ack <sequence>... --agent <name>` — advance the ack cursor.
- At-least-once BY CONSTRUCTION (re-query returns everything past the cursor);
  dedupe by journal sequence; survives restart (durable SQLite journal); no
  lost-event window (the journal, not an ephemeral broadcaster, is the source
  of truth).
- What lands in an agent's inbox: any journaled event that is (a) a
  `comment.addressed` to that agent (implicit — being addressed needs no
  config), OR (b) matched by a hook with `deliver: inbox` naming that agent.
  The exec/webhook/append-file adapters are async side-effects layered on the
  SAME journal events; they are not what makes the inbox durable.
- **Ack-cursor sync:** the per-agent ack cursor is the one new piece of synced
  state, so an agent that reads its inbox on machine A does not re-see the same
  events on machine B — it uses the same LWW-by-(agent, sequence) discipline as
  the rest of the sync layer (FR-18). This is what resolves "notify only where
  the inbox lives": the inbox is derivable on ANY replica from the synced
  journal, and the cursor keeps it consistent.

### FR-19.5 MCP surface
New MCP tools mirroring the CLI: `mtix_inbox` (list), `mtix_inbox_wait`
(long-poll with a timeout capped to the MCP client's tool timeout; the
tool documents that callers should re-invoke on timeout), `mtix_inbox_ack`,
`mtix_comment` gains the `to` parameter. This lets a Claude-based agent
block on notifications as an ordinary tool call — the enabling piece for
harnesses that cannot hold sockets.

### FR-19.6 Loop prevention
Every event carries `origin-agent` (from the session/claim identity or
`--agent` flag) and `via-hook: <name>` when produced by a hook's exec
action. Guards: (a) `from-agent-not` filter; (b) events with `via-hook`
never re-trigger the same hook name; (c) global per-node rate limit
(default: max 20 hook firings per node per hour) with a loud log line
when tripped.

### FR-19.7 Observability & testing
`mtix hooks list`, `mtix hooks log [--follow]` (audit of every firing:
event id, hook name, adapter, outcome, duration), `mtix hooks fire
--sample <event.json> [--dry-run]` for testing configs without real
mutations.

## 3. Non-functional requirements
- NFR: mutation latency impact < 5ms (hooks fully async post-commit).
- NFR: no event loss across process restarts for inbox delivery
  (journal-backed); exec/webhook are fire-with-retry, best-effort.
- NFR: hooks.yaml schema-validated on load; a bad hook config disables
  THAT hook with a warning, never the CLI.
- SECURITY (exec adapter — the sharp edge): OFF BY DEFAULT. Because
  `hooks.yaml` is human-committed and therefore SYNCED, a teammate's exec hook
  must never run on your machine without your explicit local consent. Trust is:
  1. **Per-operator and LOCAL** — recorded via `mtix hooks trust`, stored in
     local (gitignored) config, NEVER sourced from synced config. A pulled or
     synced-but-untrusted `hooks.yaml` never fires exec.
  2. **Content-pinned** — `mtix hooks trust` records the SHA-256 of the current
     `hooks.yaml` (after showing the exec hooks — argv + match — it is about to
     trust, so the operator reviews before pinning). An exec hook fires ONLY
     when the file's current hash equals the trusted hash. ANY byte change (a
     local edit, or a teammate's edit arriving via sync) invalidates the trust:
     exec silently stops with a loud log ("hooks.yaml changed since you trusted
     it — review the diff and re-run `mtix hooks trust`") until the operator
     re-reviews and re-trusts. This closes the approve-then-edit escalation — a
     teammate editing an already-trusted file cannot ride the earlier approval.

  argv-only, no shell; event content reaches exec only via env/stdin, never
  interpolated. `inbox` / `append-file` / `webhook`-to-a-configured-URL are safe
  and need no trust; only `exec` is gated. Webhook URLs must be explicitly
  configured, never derived from event content.
- Team-sync interplay (FR-18): hooks fire on LOCAL events by default;
  `include-synced: true` opt-in per hook fires on events arriving via hub
  replication (dedupe by origin event id so a comment doesn't notify on
  both the author's and the receiver's machines... it should notify ONLY
  on machines where the addressee's inbox lives — i.e., synced events
  matching `to-agent` deliver to inbox regardless, exec only if opted in).

## 4. Acceptance criteria
1. Worker loop demo: `mtix inbox --agent opus-4.8 --wait` blocks; in a
   second shell, `mtix comment HP-1.7.2 "ruling..." --to opus-4.8`
   unblocks it within 1s with the event; `ack` clears it.
2. Frontier flow demo: worker runs `mtix done HP-1.7.1`; an append-file
   hook writes one line+JSON to `FRONTIER-INBOX.md` within 1s.
3. Exec wake demo: `status.changed→done` on a node under HP-1 triggers a
   sample script exactly once, with valid `MTIX_EVENT` JSON on stdin.
4. Loop guard: a hook whose exec posts a comment does not retrigger
   itself; the rate limiter trips and logs when forced.
5. Kill -9 during delivery: after restart, undelivered inbox events
   surface; no duplicates after ack (idempotence by sequence id).
6. All of the above via MCP tools as well as CLI.

## 5. Non-goals (this FR)
- No push to remote/mobile (webhook covers integrators).
- No scheduling/cron (separate concern).
- No hook-driven automatic state transitions (hooks observe and notify;
  they do not mutate mtix state themselves — an exec script MAY call the
  CLI, subject to loop guards).

## 6. HyperSWE integration note
The orchestrator (HYPERSWE-ORCHESTRATOR-DESIGN.md §5, swe repo) consumes
exactly this surface: worker parks on `mtix_inbox_wait`; frontier rulings
are `comment --to`; the live-progress page adds inbox depth per agent.
The current file-based channel (HumanPseudo/RULINGS.md) migrates to
addressed comments once FR-19 lands, with append-file adapters keeping a
human-readable audit trail.
