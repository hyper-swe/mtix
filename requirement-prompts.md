# mtix — Prompt Chain Architecture

## The Core Insight

Traditional issue trackers store requirements at the human level — a Jira ticket says "Fix the login timeout bug." An LLM picks this up, but the ticket alone doesn't carry enough structured context for the LLM to produce accurate code. The LLM has to guess, hallucinate context, or ask the human for clarification.

mtix solves this by making **every node in the hierarchy carry a prompt/requirement**, and when an LLM picks up any task, it receives the **full ancestor chain** from root to leaf — a complete context pipeline that narrows from business intent down to specific implementation instruction.

---

## The Vision: Hierarchical Context Propagation

When a coding agent picks up task `I-1.1.2.1`, it doesn't just see that one micro issue. mtix assembles the **full context chain**:

```
S-1   (Story)       "As a user, I want secure authentication so I can trust the platform"
  ↓ context narrows
E-1   (Epic)        "Implement JWT-based auth with refresh tokens and session management"
  ↓ context narrows
I-1   (Issue)       "Fix: Login times out after 30s on slow networks — users see blank screen"
  ↓ LLM picks up I-1, creates a prompt to solve it
I-1.1 (Micro)       PROMPT: "Investigate the timeout in src/auth/login.go. The HTTP client
                     uses a hardcoded 30s timeout. Need to: (1) make it configurable,
                     (2) add retry logic, (3) show loading state to user during retry."
  ↓ LLM decomposes into sub-tasks
I-1.1.1 (Micro)     "Make timeout configurable via env var AUTH_TIMEOUT_SEC"
I-1.1.2 (Micro)     PROMPT: "Add exponential backoff retry to auth API calls. Max 3 retries.
                     Use the existing retry package in pkg/http/retry.go as reference."
  ↓ LLM discovers sub-work while implementing
I-1.1.2.1 (Micro)   "The retry package doesn't support context cancellation. Fix it first."
```

When the LLM starts working on `I-1.1.2.1`, mtix feeds it:

```
CONTEXT CHAIN (root → leaf):
━━━━━━━━━━━━━━━━━━━━━━━━━━━

[STORY S-1] Secure authentication for user trust
[EPIC  E-1] JWT auth with refresh tokens and session management
[ISSUE I-1] Login timeout on slow networks — blank screen bug
[PROMPT I-1.1] Investigate timeout in src/auth/login.go. HTTP client hardcoded 30s.
               Need: (1) configurable timeout, (2) retry logic, (3) loading state.
[PROMPT I-1.1.2] Add exponential backoff retry. Max 3 retries. Reference: pkg/http/retry.go
[TASK  I-1.1.2.1] Fix retry package to support context cancellation.
                   ← YOU ARE HERE
```

The LLM now understands:
- **WHY** it's doing this (user trust, secure auth)
- **WHAT** the actual bug is (timeout on slow networks)
- **HOW** the parent task approaches the fix (configurable timeout + retry + UI)
- **WHERE** the reference code lives (pkg/http/retry.go)
- **WHAT SPECIFICALLY** it needs to do (add context cancellation to retry package)

This is fundamentally different from just seeing "Fix retry package to support context cancellation" in isolation.

---

## What Changes in the Data Model

### New Field: `prompt`

Every node gains a `prompt` field — distinct from `title` and `description`:

| Field | Purpose | Who writes it | Example |
|-------|---------|---------------|---------|
| `title` | Short human-readable summary | Human or LLM | "Fix login timeout bug" |
| `description` | Detailed context, acceptance criteria | Human or LLM | "Users on slow networks see a blank screen..." |
| `prompt` | **LLM-facing instruction** — the precise directive an agent should follow | LLM (or human crafting LLM instructions) | "Investigate timeout in src/auth/login.go. The HTTP client uses hardcoded 30s. Need to: (1) make configurable, (2) add retry, (3) show loading state." |

The `prompt` field is the **key innovation**. It captures what the LLM decided to do at each level, creating a chain of increasingly specific instructions.

### How Prompts Flow Through the Hierarchy

**Human-created nodes (Stories, Epics, Issues):** The `prompt` field is optional. These nodes carry `title` + `description` which provide business context. If a human wants to be prescriptive about how an LLM should approach the work, they can write a prompt too.

**LLM-created nodes (Micro Issues, Sub-micros):** The `prompt` field is the primary content. When an LLM decomposes work, its reasoning about "how to solve this" becomes the prompt for the child node. This is the LLM's plan — captured as a traceable artifact.

```
I-1 (Human writes)
├── title: "Fix login timeout bug"
├── description: "Users on slow networks see blank screen after 30s..."
├── prompt: (empty — or human writes "Focus on retry logic, not UI changes")
│
└── I-1.1 (LLM creates after analyzing I-1)
    ├── title: "Implement timeout fix with retry and loading state"
    ├── prompt: "Investigate timeout in src/auth/login.go. HTTP client uses
    │           hardcoded 30s timeout. Plan: (1) make timeout configurable via
    │           AUTH_TIMEOUT_SEC env var, (2) add exponential backoff retry with
    │           max 3 attempts, (3) add loading spinner in LoginForm.tsx during
    │           retry. Reference: pkg/http/retry.go for retry patterns."
    │
    ├── I-1.1.1 (LLM decomposes)
    │   ├── title: "Make timeout configurable"
    │   └── prompt: "Add AUTH_TIMEOUT_SEC env var. Default 30s. Read in
    │               src/auth/config.go and pass to http.Client in login.go:42."
    │
    └── I-1.1.2 (LLM decomposes)
        ├── title: "Add retry logic to auth API calls"
        ├── prompt: "Use pkg/http/retry.go as base. Add exponential backoff
        │           (1s, 2s, 4s). Wrap the auth POST in login.go:58 with
        │           retry.Do(). Ensure context propagation for cancellation."
        │
        └── I-1.1.2.1 (LLM discovers while working)
            ├── title: "Fix retry package context cancellation"
            └── prompt: "pkg/http/retry.go:Do() doesn't accept context.Context.
                        Add ctx parameter, check ctx.Err() between retries,
                        return ctx.Err() if cancelled. Update all 3 callers."
```

---

## The Context Assembly Command

### `mtix context <ID>` — Assemble the Full Chain

When an LLM is about to work on a node, it calls:

```bash
mtix context I-1.1.2.1 --json
```

This returns the **assembled context chain** — every ancestor from root to the target node, with their prompts:

```json
{
  "target": "PROJ-1.1.1.2.1",
  "chain": [
    {
      "id": "PROJ-1",
      "depth": 0,
      "tier": "story",
      "title": "Secure authentication for user trust",
      "description": "As a user, I want secure authentication...",
      "prompt": null,
      "status": "in_progress"
    },
    {
      "id": "PROJ-1.1",
      "depth": 1,
      "tier": "epic",
      "title": "JWT auth with refresh tokens",
      "description": "Implement JWT-based auth with refresh tokens and session management",
      "prompt": null,
      "status": "in_progress"
    },
    {
      "id": "PROJ-1.1.1",
      "depth": 2,
      "tier": "issue",
      "title": "Fix login timeout bug",
      "description": "Users on slow networks see blank screen after 30s timeout...",
      "prompt": "Focus on retry logic approach, not UI-only changes",
      "status": "in_progress"
    },
    {
      "id": "PROJ-1.1.1.1",
      "depth": 3,
      "tier": "micro",
      "title": "Implement timeout fix with retry and loading state",
      "prompt": "Investigate timeout in src/auth/login.go. HTTP client uses hardcoded 30s timeout. Plan: (1) make timeout configurable, (2) add exponential backoff retry, (3) add loading spinner.",
      "status": "in_progress"
    },
    {
      "id": "PROJ-1.1.1.1.2",
      "depth": 4,
      "tier": "micro",
      "title": "Add retry logic to auth API calls",
      "prompt": "Use pkg/http/retry.go as base. Add exponential backoff (1s, 2s, 4s). Wrap auth POST in login.go:58 with retry.Do(). Ensure context propagation.",
      "status": "in_progress"
    },
    {
      "id": "PROJ-1.1.1.1.2.1",
      "depth": 5,
      "tier": "micro",
      "title": "Fix retry package context cancellation",
      "prompt": "pkg/http/retry.go:Do() doesn't accept context.Context. Add ctx parameter, check ctx.Err() between retries. Update all 3 callers.",
      "status": "open",
      "code_refs": [{"file": "pkg/http/retry.go", "fn": "Do"}],
      "is_target": true
    }
  ],
  "siblings": [
    {"id": "PROJ-1.1.1.1.2.2", "title": "Add retry unit tests", "status": "open"}
  ],
  "blocking_deps": [],
  "assembled_prompt": "[HUMAN-AUTHORED] ## Context\n\nYou are working on a secure authentication system (JWT with refresh tokens). A bug was reported: login times out after 30s on slow networks, showing a blank screen.\n\n[LLM-GENERATED] ## Approach\n\nThe fix involves three parts: (1) configurable timeout, (2) retry logic with exponential backoff, (3) loading state UI. You are implementing part (2): retry logic.\n\n[ANNOTATION by vimal] Use jittered backoff instead of exponential — see https://aws.amazon.com/builders-library/timeouts-retries-and-backoff-with-jitter/\n\n[LLM-GENERATED] ## Your Task\n\nThe retry package at `pkg/http/retry.go` has a `Do()` function that doesn't accept `context.Context`. You need to:\n1. Add a `ctx context.Context` parameter to `Do()`\n2. Check `ctx.Err()` between retry attempts\n3. Return `ctx.Err()` if the context is cancelled\n4. Update all 3 existing callers of `Do()` to pass context\n\n## Reference\n- Retry package: `pkg/http/retry.go`\n- Auth login: `src/auth/login.go:58`"
}
```

The `assembled_prompt` field is the **killer feature** — mtix automatically stitches the chain into a single, coherent prompt that gives the LLM full context from business intent down to specific implementation task.

### Assembly Rules

The context assembly follows these rules:

1. **Walk the ancestor chain** from root to target node
2. **For each ancestor**, include: tier label, title, and prompt (if present). Skip description for ancestors more than 2 levels above target (to save tokens).
3. **Include unresolved prompt annotations** — if any node in the chain has unresolved annotations (see requirement-ui.md), they MUST be appended to that node's prompt in the chain and included in the `assembled_prompt` output. Annotations provide critical human steering that the LLM must see.
4. **Include the target node's** full details: title, prompt, description, code_refs, acceptance criteria
5. **Include siblings** of the target (so the LLM knows what else is being worked on in parallel)
6. **Include blocking dependencies** (so the LLM knows what might be waiting on this work)
7. **Generate `assembled_prompt`** — a natural-language stitched prompt that reads like a briefing document

### Token Budget Awareness

The context command MUST support a `--max-tokens` flag:

```bash
mtix context I-1.1.2.1 --max-tokens 2000
```

When the chain exceeds the token budget:
- **Always include** the target node in full
- **Always include** the immediate parent's prompt
- **Summarize** distant ancestors (just tier + title, no prompt/description)
- **Truncate** long prompts at ancestor levels, keeping the first sentence

This ensures the most relevant context is always preserved even for deep hierarchies.

---

## How This Changes LLM Workflow

### Before mtix (traditional tracker)

```
LLM receives: "Fix retry package context cancellation"
LLM thinks: "What retry package? What context? What's the broader goal?"
LLM: *reads random files, guesses at intent, may go off-track*
```

### With mtix (prompt chain)

```
LLM calls: mtix context PROJ-1.1.1.1.2.1 --json
LLM receives: Full chain from "secure auth" → "timeout bug" → "retry approach" → "this specific fix"
LLM thinks: "I understand the full picture. I know exactly what to do and why."
LLM: *makes targeted, accurate changes aligned with the broader plan*
```

### The Prompt Capture Workflow

When an LLM picks up a task and decomposes it, mtix captures its reasoning:

```bash
# Step 1: LLM reads the issue
mtix context PROJ-1.1.1 --json

# Step 2: LLM creates a micro issue with its analysis/plan as the prompt
mtix micro "Implement timeout fix with retry and loading state" \
  --under PROJ-1.1.1 \
  --prompt "Investigate timeout in src/auth/login.go. HTTP client uses hardcoded
            30s timeout. Plan: (1) make timeout configurable via AUTH_TIMEOUT_SEC,
            (2) add exponential backoff retry max 3 attempts, (3) add loading
            spinner in LoginForm.tsx during retry."

# Step 3: LLM decomposes further
mtix micro "Make timeout configurable" --under PROJ-1.1.1.1 \
  --prompt "Add AUTH_TIMEOUT_SEC env var. Default 30s. Read in src/auth/config.go
            and pass to http.Client in login.go:42."

mtix micro "Add retry logic" --under PROJ-1.1.1.1 \
  --prompt "Use pkg/http/retry.go as base. Exponential backoff (1s, 2s, 4s).
            Wrap auth POST in login.go:58. Ensure context propagation."

mtix micro "Add loading spinner during retry" --under PROJ-1.1.1.1 \
  --prompt "In LoginForm.tsx, add loading state when auth call starts. Show
            spinner with retry count. Clear on success or final failure."
```

Each prompt is now a **traceable artifact** — you can see exactly what the LLM planned, audit it, and future LLMs can build on it.

---

## Design Implications for mtix

### 1. Node Data Model Changes

Add `prompt` as a first-class field alongside `title` and `description`:

```go
type Node struct {
    // ... existing fields ...

    // Content
    Title       string           `json:"title"`        // Short human summary
    Description string           `json:"description"`  // Detailed context (human-facing)
    Prompt      string           `json:"prompt"`        // LLM-facing instruction/plan
    Activity    []ActivityEntry  `json:"activity"`      // Unified activity stream — see FR-3.6 in REQUIREMENTS.md for ActivityEntry definition
    Acceptance  string           `json:"acceptance"`    // Done criteria

    // ... rest of fields ...
}
```

### 2. CLI Changes

**`mtix create` and `mtix micro`** gain a `--prompt` flag:

```bash
mtix micro "Title" --under PARENT --prompt "Detailed LLM instruction..."
```

**`mtix context`** becomes the primary command LLMs call before starting work:

```bash
mtix context <ID> [--json] [--max-tokens N] [--assembled]
```

- `--json` — machine-readable chain
- `--max-tokens N` — truncate to fit token budget
- `--assembled` — return only the stitched `assembled_prompt` text (no JSON wrapper)

### 3. REST API Changes

New field in node creation/update payloads:

```json
POST /api/v1/nodes
{
  "title": "Add retry logic",
  "parent_id": "PROJ-1.1.1.1",
  "prompt": "Use pkg/http/retry.go as base. Exponential backoff..."
}
```

New endpoint for context assembly:

```
GET /api/v1/nodes/{id}/context?max_tokens=2000&format=assembled
```

### 4. gRPC Changes

New `Context` RPC and `ContextRequest` message:

```protobuf
rpc GetContext(ContextRequest) returns (ContextResponse);

message ContextRequest {
  string id = 1;
  int32 max_tokens = 2;      // Optional token budget
  bool assembled_only = 3;   // Return only stitched prompt text
}

message ContextResponse {
  string target_id = 1;
  repeated ChainNode chain = 2;
  repeated NodeSummary siblings = 3;
  repeated Dependency blocking_deps = 4;
  string assembled_prompt = 5;   // The stitched briefing
}

message ChainNode {
  string id = 1;
  int32 depth = 2;
  string tier = 3;
  string title = 4;
  string prompt = 5;
  string description = 6;
  string status = 7;
  bool is_target = 8;
  repeated Annotation annotations = 9;  // Unresolved prompt annotations (human steering)
}

message Annotation {
  string id = 1;
  string author = 2;
  string text = 3;
  string created_at = 4;
  bool resolved = 5;
}
```

### 5. Python SDK Changes

```python
# Get full context chain before working
ctx = client.context("PROJ-1.1.1.1.2.1", max_tokens=2000)

# Access the assembled prompt (ready to inject into LLM)
llm_prompt = ctx.assembled_prompt

# Or access individual chain nodes
for node in ctx.chain:
    print(f"[{node.tier}] {node.title}")
    if node.prompt:
        print(f"  Prompt: {node.prompt}")
```

### 6. Web UI Changes

The Detail Panel gains a **Context Chain** section:

```
┌─────────────────────────────────────────────┐
│  PROJ-1.1.1.1.2.1                          │
│  Fix retry package context cancellation     │
│                                             │
│  ┌─ Context Chain ────────────────────────┐ │
│  │ S  Secure authentication               │ │
│  │ E  JWT auth with refresh tokens        │ │
│  │ I  Fix login timeout bug               │ │
│  │ μ  Implement timeout fix + retry       │ │
│  │    "Investigate timeout in login.go.   │ │
│  │     HTTP client hardcoded 30s..."      │ │
│  │ μ  Add retry logic to auth calls       │ │
│  │    "Use pkg/http/retry.go as base..."  │ │
│  │ ► THIS: Fix retry context cancellation │ │
│  │    "Add ctx parameter to Do(),         │ │
│  │     check ctx.Err() between retries"   │ │
│  └────────────────────────────────────────┘ │
│                                             │
│  Prompt:                                    │
│  pkg/http/retry.go:Do() doesn't accept     │
│  context.Context. Add ctx parameter, check  │
│  ctx.Err() between retries, return ctx.Err()│
│  if cancelled. Update all 3 callers.        │
└─────────────────────────────────────────────┘
```

### 7. Prompt Versioning (Future Enhancement)

When an LLM updates a node's prompt (refining its approach), the system SHOULD keep a history:

```json
{
  "prompt": "current prompt text",
  "prompt_history": [
    {
      "text": "original prompt text",
      "author": "agent-claude-1",
      "timestamp": "2026-02-27T10:30:00Z",
      "reason": "Initial decomposition"
    },
    {
      "text": "revised prompt text",
      "author": "agent-claude-2",
      "timestamp": "2026-02-27T11:15:00Z",
      "reason": "Refined after discovering retry package limitations"
    }
  ]
}
```

This creates an audit trail of how the LLM's understanding evolved.

---

## Why This Matters

### 1. Accuracy Improvement
Each LLM invocation gets precisely scoped context. No guessing, no hallucinating about intent. The parent prompts tell the LLM exactly what approach was chosen and why.

### 2. Continuity Across Sessions
When an LLM session times out or a different model picks up the work, the prompt chain preserves all the reasoning. No context is lost between sessions.

### 3. Human Oversight
Humans can read the prompt chain to understand what the LLM planned and audit its reasoning before the code is even written. Bad plans can be caught and redirected early.

### 4. Prompt Optimization
Over time, teams can analyze which prompt patterns at each tier level produce the best code quality. The prompt chain becomes training data for improving LLM-driven development.

### 5. Debugging Failed Implementations
When code doesn't work, you can trace back through the prompt chain to find where the reasoning went wrong — was it a bad story definition? A wrong decomposition? A misleading parent prompt?

---

## Summary of New Requirements

| Requirement | Description |
|-------------|-------------|
| **FR-PROMPT-1** | Every node MUST support a `prompt` field (string, optional, markdown) distinct from `title` and `description` |
| **FR-PROMPT-2** | `mtix context <ID>` MUST assemble the full ancestor chain from root to target, including prompts at every level |
| **FR-PROMPT-3** | The context response MUST include an `assembled_prompt` field that stitches the chain into a coherent LLM briefing |
| **FR-PROMPT-4** | `mtix context` MUST support `--max-tokens` for token-budget-aware truncation |
| **FR-PROMPT-5** | `mtix create` and `mtix micro` MUST accept a `--prompt` flag |
| **FR-PROMPT-6** | The REST API MUST expose `GET /nodes/{id}/context` with token budget and format options |
| **FR-PROMPT-7** | The gRPC API MUST expose a `GetContext` RPC |
| **FR-PROMPT-8** | The Python SDK MUST provide a `client.context(id)` method returning the chain and assembled prompt |
| **FR-PROMPT-9** | The Web UI Detail Panel MUST display the context chain for any selected node |
| **FR-PROMPT-10** | Token-budget truncation MUST prioritize: (1) target node in full, (2) immediate parent prompt, (3) ancestor titles, (4) distant ancestor prompts |
| **FR-PROMPT-11** | (Future) Prompt versioning — the system SHOULD track prompt revision history with author, timestamp, and reason |
| **FR-PROMPT-12** | The `assembled_prompt` MUST include source attribution markers: `[HUMAN-AUTHORED]`, `[LLM-GENERATED]`, `[ANNOTATION by {author}]` — defense-in-depth against prompt injection (see FR-12.3a in REQUIREMENTS.md) |
