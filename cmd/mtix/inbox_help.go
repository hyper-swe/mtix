// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// inboxLongHelp preserves the count, wait and untrusted-envelope CLI contract.
const inboxLongHelp = `Show comments addressed to an agent (via 'mtix comment --to <agent>'),
oldest first, past the agent's ack cursor.

--count prints the pending event count; --count --all prints every retained
addressed event, including acknowledged events. --json returns an object with agent and integer
pending and total fields. Zero is success (exit 0). Comments and hook deliveries
count once per journal event. Count never acknowledges, moves the cursor or
refreshes session last_seen; it skips auto-import and makes no hub contact.
Ordinary project startup, including schema/index installation, still applies.
Do not combine active --count with explicitly supplied --wait or --format,
including --wait=false or --format=. Any --all flag requires active --count;
--count=false alone keeps listing behavior. Count flags apply only to inbox,
not its ack or lease subcommands.

With --wait, block until a new addressed event arrives — the primitive a worker's
outer loop parks on between tasks — or until --timeout seconds elapse. Exit 0
when events are returned; exit 5 on an empty timeout (so a loop can distinguish
"woke with work" from "nothing yet").

--format emits agent-ready text instead of the plain listing (FR-20 §9,
"delivery terminates in the prompt"):
  prompt   a complete opening prompt for a cold-started agent — the events
           quoted in an untrusted-message envelope plus the ack/reply contract. A wake exec launches the
           harness CLI with this as the prompt.
  context  a compact block for harness context-injection hooks
           (session-start / prompt-submit).
Both print NOTHING when the inbox is empty, so a wake script's idempotency
check is: payload=$(mtix inbox --agent X --format prompt); [ -z "$payload" ].
Reads never ack — acknowledgement stays explicit.`
