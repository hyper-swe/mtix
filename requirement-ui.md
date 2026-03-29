# mtix — UI Requirements: Linear-Style Interface

## Design Philosophy

mtix's web UI should feel like Linear — fast, keyboard-driven, minimal chrome, and zero unnecessary clicks. The UI serves two audiences simultaneously: **humans managing work** and **humans overseeing LLM agents**. Every interaction should be snappy (sub-100ms perceived latency) with optimistic updates.

---

## 1. Core UI Principles

### Speed
- **Optimistic updates** — UI updates immediately on action, syncs in background
- **No full-page reloads** — SPA with client-side routing
- **Keyboard-first** — every action reachable via keyboard shortcut
- **Prefetching** — hover over a node? Its children are already loading
- **Minimal payload** — list views load summaries, detail view loads on select

### Simplicity
- **No visual clutter** — clean whitespace, muted colors, information-dense but not crowded
- **Progressive disclosure** — show title + status by default, expand for prompt/description/activity
- **Contextual actions** — right actions appear at the right time (no toolbar with 20 buttons)
- **Consistent patterns** — every list, every node, every panel follows the same interaction model

### Keyboard Shortcuts (Linear-inspired)

| Key | Action |
|-----|--------|
| `c` | Create new node (context-aware: under selected parent) |
| `m` | Create micro issue under selected node |
| `Enter` | Open/select node |
| `Esc` | Close panel / go back |
| `j` / `k` | Move down / up in list |
| `l` / `h` | Expand / collapse tree node |
| `x` | Mark done |
| `i` | Set in_progress |
| `d` | Defer |
| `e` | Edit title inline |
| `p` | Edit prompt inline |
| `Cmd+K` | Command palette (search anything) |
| `Cmd+/` | Show all shortcuts |
| `1-5` | Set priority (1=critical, 2=high, 3=medium, 4=low, 5=backlog) |
| `s` | Cycle status |
| `Tab` | Move focus: Tree → List → Detail |

---

## 2. Layout

### Two Primary Views

**View A: Board View (default for human work management)**

```
┌──────────────────────────────────────────────────────────────────────────┐
│  mtix    PROJ ▼    [Cmd+K] Search...                    ● agent-claude  │
├──────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  ┌─ Sidebar (collapsible) ─┐  ┌─ Main Content ───────────────────────┐  │
│  │                         │  │                                       │  │
│  │  ▶ Stories              │  │  PROJ-42.1.3  Add form validation     │  │
│  │    ▼ S-1 User Auth      │  │  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━     │  │
│  │      ▼ E-1.1 Login      │  │                                       │  │
│  │        ● I-1.1.1 ✓      │  │  in_progress  P2  agent-claude        │  │
│  │        ● I-1.1.2        │  │  ████████░░░░ 66%                     │  │
│  │        ○ I-1.1.3        │  │                                       │  │
│  │      ▶ E-1.2 Signup     │  │  ┌─ Prompt ──────────────────────┐    │  │
│  │      ○ E-1.3 OAuth      │  │  │ Investigate timeout in        │    │  │
│  │    ▶ S-2 Payments       │  │  │ src/auth/login.go. HTTP       │    │  │
│  │    ▶ S-3 Dashboard      │  │  │ client uses hardcoded 30s...  │    │  │
│  │                         │  │  │                          [Edit]│    │  │
│  │  ─────────────          │  │  └───────────────────────────────┘    │  │
│  │  ▶ Views                │  │                                       │  │
│  │    All Issues            │  │  ┌─ Children ───────────────────┐    │  │
│  │    My Work              │  │  │ ✓ .1 Make timeout config      │    │  │
│  │    Agent Activity       │  │  │ ● .2 Add retry logic          │    │  │
│  │    Stale                │  │  │ ○ .3 Add loading spinner      │    │  │
│  │                         │  │  │                    [+ Add micro]│    │  │
│  │  ─────────────          │  │  └───────────────────────────────┘    │  │
│  │  ▶ Filters              │  │                                       │  │
│  │    Status ▼             │  │  ┌─ Context Chain ───────────────┐    │  │
│  │    Priority ▼           │  │  │ S  User Auth                  │    │  │
│  │    Assignee ▼           │  │  │ E  Login flow                 │    │  │
│  │    Type ▼               │  │  │ I  Fix timeout bug            │    │  │
│  │                         │  │  │ ► THIS: Add form validation   │    │  │
│  └─────────────────────────┘  │  └───────────────────────────────┘    │  │
│                               │                                       │  │
│                               │  Description  Activity  Deps          │  │
│                               │  ─────────────────────────────────    │  │
│                               │  Users on slow networks see blank...  │  │
│                               └───────────────────────────────────────┘  │
├──────────────────────────────────────────────────────────────────────────┤
│  S-1 > E-1.1 > I-1.1.2 > I-1.1.2.3  ·  3 open  ·  Last sync 30s ago  │
└──────────────────────────────────────────────────────────────────────────┘
```

**View B: Agent Dashboard (for monitoring LLM activity)**

```
┌──────────────────────────────────────────────────────────────────────────┐
│  mtix    Agent Activity                                                  │
├──────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  ┌─ agent-claude ──────────────────────────────────────────────────┐     │
│  │  Status: working    Session: 23 min    Heartbeat: 12s ago       │     │
│  │                                                                  │     │
│  │  Working on: PROJ-42.1.3.2 "Add retry logic to auth calls"     │     │
│  │  ████████████░░░░ 75%                                           │     │
│  │                                                                  │     │
│  │  Recent actions:                                                 │     │
│  │  10:42  ✓ Done   PROJ-42.1.3.2.1 "Fix retry context cancel"    │     │
│  │  10:38  + Comment "Using exponential backoff 1s, 2s, 4s"       │     │
│  │  10:35  + Create PROJ-42.1.3.2.2 "Add retry unit tests"        │     │
│  │  10:30  ▶ Claim  PROJ-42.1.3.2                                 │     │
│  └──────────────────────────────────────────────────────────────────┘     │
│                                                                          │
│  ┌─ agent-gpt4 ───────────────────────────────────────────────────┐     │
│  │  Status: idle      Session: —         Heartbeat: 5 min ago      │     │
│  │  No active work. Last completed: PROJ-42.2.1 (15 min ago)      │     │
│  └──────────────────────────────────────────────────────────────────┘     │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Prompt Editing & Human Steering

This is the core human-in-the-loop feature. Humans can read what the LLM planned (the prompt), edit it, add instructions, and trigger a rerun of affected micro issues.

### 3.1 Inline Prompt Editor

When a user clicks [Edit] on a prompt or presses `p`:

```
┌─ Prompt ─────────────────────────────────────────────┐
│                                                       │
│  Investigate timeout in src/auth/login.go. HTTP       │
│  client uses hardcoded 30s timeout.                   │
│                                                       │
│  Plan:                                                │
│  (1) Make timeout configurable via AUTH_TIMEOUT_SEC   │
│  (2) Add exponential backoff retry, max 3 attempts    │
│  (3) Add loading spinner in LoginForm.tsx              │
│                                                       │
│  ┌─ Human Addition ────────────────────────────────┐  │
│  │ IMPORTANT: Also handle the case where the       │  │
│  │ network drops mid-request. Use context.WithCancel│  │
│  │ and clean up pending requests on unmount.        │  │
│  └─────────────────────────────────────────────────┘  │
│                                                       │
│  [Save]  [Save & Rerun Children ▼]  [Cancel]         │
└───────────────────────────────────────────────────────┘
```

The editor:
- Shows the existing prompt as editable markdown
- Supports a distinct "Human Addition" block that is visually separated (so it's clear what the LLM wrote vs. what the human added)
- **Save** — saves the prompt edit, no further action
- **Save & Rerun Children** — saves the prompt, then marks all descendant micro issues for rerun (see 3.2)

### 3.2 Rerun: Cascading Invalidation

When a human edits a prompt at node `I-1.1.2` and clicks "Save & Rerun Children", the system must:

**Step 1: Mark descendants as invalidated**

All descendants of `I-1.1.2` get a new status: `invalidated`. This means "the parent prompt changed, this work may no longer be correct."

```
I-1.1.2       [in_progress] ← prompt was edited here
├── I-1.1.2.1 [invalidated] ← was "done", now needs re-evaluation
├── I-1.1.2.2 [invalidated] ← was "in_progress"
│   ├── I-1.1.2.2.1 [invalidated]
│   └── I-1.1.2.2.2 [invalidated]
└── I-1.1.2.3 [invalidated] ← was "open"
```

**Step 2: Rerun options**

The user chooses what to do with invalidated children:

| Option | Behavior |
|--------|----------|
| **Rerun all** | All descendants are reset to `open`. The next LLM that picks up `I-1.1.2` will re-decompose with the new prompt. Existing micro issues are preserved but marked open for re-evaluation. |
| **Rerun open only** | Only descendants that were `open` or `in_progress` are reset. `done` items are kept (assumed still valid). |
| **Delete & re-decompose** | All descendants are soft-deleted (recoverable for 30 days). The LLM starts fresh with the new prompt, creating new micro issues from scratch. Requires confirmation if >10 descendants. |
| **Manual review** | Descendants are marked `invalidated` and the human reviews each one, deciding to keep, reset, or delete individually. |

**Step 3: Notification**

If an LLM agent is currently working on an invalidated node, the system:
- Sends a WebSocket event: `{type: "node.updated", node_id: "PROJ-1.1.2.2", fields: {status: "invalidated", invalidation_reason: "Parent prompt edited"}}`
- A batch `nodes.invalidated` event is also sent for the UI tree refresh (see FR-7.5a)
- The agent should stop work, re-read the context chain, and decide whether to continue or re-plan

### 3.3 Prompt Annotations

Humans can add **annotations** to any prompt without replacing it — like comments on a pull request:

```
┌─ Prompt ─────────────────────────────────────────────┐
│  Use pkg/http/retry.go as base. Exponential backoff   │
│  (1s, 2s, 4s). Wrap auth POST in login.go:58.        │
│                                                       │
│  💬 vimal: Don't use exponential — use jittered       │
│     backoff instead. See https://aws.amazon.com/...   │
│                                                       │
│  💬 vimal: Also add circuit breaker pattern. Check    │
│     if pkg/http/circuit.go already exists.            │
└───────────────────────────────────────────────────────┘
```

Annotations are:
- Appended (not replacing the original prompt)
- Attributed to the author
- Timestamped
- Visible to LLMs when they read the context chain
- Can be resolved/dismissed (like PR comments)

---

## 4. Node Data Model Additions

### New fields required for UI/rerun features:

```go
type Node struct {
    // ... existing fields ...

    // Prompt steering
    Prompt          string           `json:"prompt"`
    Annotations     []Annotation   `json:"annotations,omitempty"`

    // Rerun tracking
    InvalidatedAt   *time.Time       `json:"invalidated_at,omitempty"`
    InvalidatedBy   string           `json:"invalidated_by,omitempty"`
    InvalidationReason string        `json:"invalidation_reason,omitempty"`
    PreviousStatus  Status           `json:"previous_status,omitempty"` // Status before invalidation
}

type Annotation struct {
    ID        string    `json:"id"`
    Author    string    `json:"author"`
    Text      string    `json:"text"`
    CreatedAt time.Time `json:"created_at"`
    Resolved  bool      `json:"resolved"`
}
```

### New status: `invalidated`

Added to the Status enum:

```
open | in_progress | blocked | done | deferred | cancelled | invalidated

Note: `blocked` is NOT a manually-settable status. It is auto-managed: when a node has unresolved blocking dependencies, the system automatically sets `blocked` and saves `previous_status`. When all blockers resolve, the system auto-reverts to `previous_status`. See FR-3.8 in REQUIREMENTS.md.
```

An `invalidated` node:
- Retains all its content (prompt, description, code refs, etc.)
- Preserves its `previous_status` so it can be restored if the invalidation is reversed
- Shows a distinct visual indicator in the UI (yellow warning icon)
- Is excluded from progress calculations (treated like `cancelled` in the denominator)

---

## 5. CLI Commands for Rerun

```bash
# Edit a prompt from CLI (opens $EDITOR or accepts inline)
mtix prompt PROJ-42.1.3 "New prompt text here"
mtix prompt PROJ-42.1.3 --edit              # Opens $EDITOR

# Add an annotation to a prompt
mtix annotate PROJ-42.1.3 "Use jittered backoff instead of exponential"

# Invalidate and rerun descendants
mtix rerun PROJ-42.1.3                      # Interactive: choose rerun strategy
mtix rerun PROJ-42.1.3 --all                # Reset all descendants to open
mtix rerun PROJ-42.1.3 --open-only          # Only reset open/in_progress
mtix rerun PROJ-42.1.3 --delete             # Delete descendants, start fresh
mtix rerun PROJ-42.1.3 --review             # Mark as invalidated for manual review

# Restore an invalidated node (undo the invalidation)
mtix restore PROJ-42.1.3.2                  # Restore to previous_status
```

---

## 6. REST API Additions

```
# Prompt operations
PUT    /nodes/{id}/prompt              Update prompt text
POST   /nodes/{id}/prompt/annotate     Add annotation to prompt
PATCH  /nodes/{id}/prompt/annotations/{ann_id}  Resolve/unresolve annotation

# Rerun operations
POST   /nodes/{id}/rerun               Trigger rerun (body: {strategy: "all"|"open_only"|"delete"|"review"})
POST   /nodes/{id}/restore             Restore from invalidated to previous_status
```

---

## 7. WebSocket Events for Real-Time UI

The UI subscribes to WebSocket events for live updates:

```json
// Node created
{"type": "node.created", "node": {...}, "parent_id": "PROJ-42.1.3"}

// Node updated (status, prompt, priority, etc.)
{"type": "node.updated", "node_id": "PROJ-42.1.3", "fields": {"status": "done"}}

// Progress changed (rolls up)
{"type": "progress.changed", "node_id": "PROJ-42.1", "progress": 0.75}

// Prompt edited
{"type": "prompt.edited", "node_id": "PROJ-42.1.3", "editor": "user@example.com"}

// Annotation added
{"type": "prompt.annotated", "node_id": "PROJ-42.1.3", "annotation": {...}}

// Node soft-deleted (per-node event — emitted for each deleted node)
{"type": "node.deleted", "node_id": "PROJ-42.1.3", "deleted_by": "user@example.com"}

// Descendants deleted (batch event — emitted once per cascade delete operation)
{"type": "nodes.deleted", "parent_id": "PROJ-42.1.3", "count": 12, "cascade": true}

// Descendants invalidated
{"type": "nodes.invalidated", "parent_id": "PROJ-42.1.3", "count": 5, "strategy": "review"}

// Agent state change
{"type": "agent.state", "agent_id": "agent-claude", "state": "working", "node_id": "PROJ-42.1.3.2"}

// Agent stuck (distinct from agent.state for UI handling — triggers Stale Board addition)
{"type": "agent.stuck", "agent_id": "agent-claude", "node_id": "PROJ-42.1.3.2"}

// Agent heartbeat
{"type": "agent.heartbeat", "agent_id": "agent-claude", "timestamp": "..."}
```

---

## 8. Special UI Components

### 8.1 Command Palette (Cmd+K)

Linear-style universal search and action palette:

```
┌─────────────────────────────────────────────────┐
│  ▸ Search nodes, run commands...                 │
│                                                  │
│  Recent                                          │
│  ● PROJ-42.1.3.2  Add retry logic               │
│  ○ PROJ-42.1.3.3  Add loading spinner            │
│                                                  │
│  Actions                                         │
│  ⊕ Create node                                   │
│  ▶ Switch view                                   │
│  ⚙ Settings                                      │
└──────────────────────────────────────────────────┘
```

Supports:
- Fuzzy search across all node titles, prompts, and descriptions
- Action execution (create, claim, done, rerun)
- Navigation to any node by ID or title
- Filter shortcuts ("status:open priority:1")

### 8.2 Breadcrumb Bar (Bottom)

Always visible, shows the path to the currently selected node:

```
S-1 User Auth  ›  E-1.1 Login  ›  I-1.1.2 Retry logic  ›  I-1.1.2.1 Fix context
────────────────────────────────────────────────────────────────────────────────
████████████████░░░░░░░░ 66% overall    ·   3 open   ·   1 agent active
```

Each breadcrumb segment is clickable for quick navigation up the tree.

### 8.3 Quick-Add Bar

At the bottom of any children list, a persistent input for fast micro issue creation:

```
┌─ Children of PROJ-42.1.3 ──────────────────────┐
│  ✓ .1 Make timeout configurable                  │
│  ● .2 Add retry logic                           │
│  ○ .3 Add loading spinner                        │
│                                                  │
│  [+ Add micro issue...]                          │
│  ┌──────────────────────────────────────────┐    │
│  │ Handle network drop mid-request          │    │
│  │                                          │    │
│  │ [Enter to create]  [Tab for prompt]      │    │
│  └──────────────────────────────────────────┘    │
└──────────────────────────────────────────────────┘
```

Pressing `Tab` in the quick-add expands a prompt field below the title.

### 8.4 Diff View for Prompt Edits

When a prompt is edited, show a diff of what changed (like a git diff):

```
┌─ Prompt Changed ─────────────────────────────────┐
│                                                   │
│  Use pkg/http/retry.go as base.                   │
│- Exponential backoff (1s, 2s, 4s).               │
│+ Jittered backoff with decorrelated strategy.     │
│+ Add circuit breaker pattern using                │
│+ pkg/http/circuit.go.                             │
│  Wrap auth POST in login.go:58.                   │
│                                                   │
│  Changed by: user@example.com at 10:45           │
│  [Rerun Children]  [Dismiss]                      │
└───────────────────────────────────────────────────┘
```

### 8.5 Inline Node Status Transitions

Hovering over a status badge reveals the valid next states:

```
  ● in_progress  →  [✓ Done]  [⏸ Defer]  [✕ Cancel]  [↻ Rerun]
```

Single click transitions. No confirmation modal for forward transitions (open→progress→done). Destructive transitions (cancel, rerun/delete) get a small confirmation popover.

### 8.6 Multi-Select & Bulk Actions

Hold `Shift` to select multiple nodes in a list, then apply bulk actions:

```
Selected: 3 nodes
[✓ Mark Done]  [◉ Set Priority ▼]  [👤 Assign ▼]  [↻ Rerun]  [✕ Cancel]
```

---

## 9. Theming & Visual Design

### Color System

| Element | Light | Dark |
|---------|-------|------|
| Background | #FAFAFA | #1A1A2E |
| Surface (cards) | #FFFFFF | #222244 |
| Text primary | #1A1A2E | #E8E8F0 |
| Text secondary | #6B7280 | #9CA3AF |
| Accent (actions) | #6366F1 (indigo) | #818CF8 |
| Done | #10B981 (green) | #34D399 |
| In-progress | #3B82F6 (blue) | #60A5FA |
| Blocked | #EF4444 (red) | #F87171 |
| Open | #6B7280 (gray-500) | #9CA3AF (gray-400) |
| Invalidated | #F59E0B (amber) | #FBBF24 |
| Deferred | #8B5CF6 (purple) | #A78BFA |
| Cancelled | #9CA3AF (gray-400) | #6B7280 (gray-500) |

### Typography
- **Font:** Inter (or system sans-serif fallback)
- **Node title:** 14px medium
- **Prompt text:** 13px regular, slightly muted
- **IDs:** 12px monospace, very muted
- **Status badges:** 11px uppercase, bold

### Density
- **Default:** Comfortable (40px row height in lists)
- **Compact:** Dense (28px row height — for power users managing hundreds of micro issues)
- User-toggleable via settings or `Cmd+D`

---

## 10. Kanban Board View

> The tree view (FR-UI-2) is the primary navigation surface, optimized for hierarchical context traversal. A Kanban board view provides a complementary perspective: status-grouped columns showing work distribution at a glance. This is especially useful for agents and humans monitoring pipeline throughput — seeing how many items are blocked, in progress, or done without navigating the tree.

### 10.1 Board Layout

**FR-UI-21a** The Kanban board MUST be accessible as a view option alongside the tree view, toggled via the sidebar navigation or command palette (`Cmd+K` → "Kanban"). The view selection MUST persist across sessions (stored in `localStorage`).

**FR-UI-21b** The board MUST display columns for each active status: **Open**, **In Progress**, **Blocked**, **Deferred**, **Done**, **Cancelled**. The **Invalidated** status MUST be visually grouped with its originating status (shown as a badge on the card, not a separate column). Empty columns MUST still render with a header to maintain spatial consistency.

**FR-UI-21c** Column headers MUST display the status name and item count (e.g., "In Progress (7)"). The count MUST update in real-time via WebSocket events (FR-UI-17).

### 10.2 Cards

**FR-UI-21d** Each card MUST display: node ID (monospace, muted), title, priority indicator (color dot or icon), and assignee (if present). Cards for parent nodes MUST show a progress bar. Cards MUST be styled consistently with the tree view row aesthetic — same typography, same status colors, same density response.

**FR-UI-21e** Clicking a card MUST select the node and open the detail panel (same behavior as clicking a tree row). The `selectedId` state MUST be shared between tree and Kanban views.

### 10.3 Drag-and-Drop Status Transitions

**FR-UI-21f** Dragging a card between columns MUST trigger a status transition via the existing service layer. Invalid transitions (per FR-3.5 state machine) MUST be rejected: the card MUST snap back to its original column with a brief error toast. Valid transitions MUST optimistically move the card, but the card MUST be visually distinct from server-confirmed cards during the confirmation window (e.g., reduced opacity, pulsing animation, or a "Confirming..." badge). This prevents safety-critical decisions based on unconfirmed state — a user MUST NOT mistake an optimistic "Done" for a server-confirmed "Done". If the server does not confirm within 3 seconds (configurable), the optimistic state MUST automatically revert with a warning toast. WebSocket server events are always authoritative and override any optimistic local state.

**FR-UI-21g** The board MUST respect the same state machine rules as the tree view — no special permissions or bypass. Destructive transitions (cancel, reopen from done) MUST show a confirmation popover before executing, consistent with FR-UI-13.

### 10.4 Filtering and Scope

**FR-UI-21h** The board MUST support scoping to a subtree via the `under` parameter (same as `mtix list --under`). When a parent node is selected in the sidebar, the board shows only its descendants. When no scope is set, the board shows root-level nodes.

**FR-UI-21i** The board MUST respect the existing `hideDone` filter toggle. When active, the Done and Cancelled columns MUST be hidden.

### 10.5 Real-Time Updates

**FR-UI-21j** The board MUST update in real-time via the same WebSocket connection used by the tree view. Node creation, status changes, and deletions MUST be reflected immediately without manual refresh. Cards MUST animate into/out of columns using the project's standard transition timing (`--transition-base`).

### 10.6 Keyboard Navigation

**FR-UI-21k** The board MUST support keyboard-first navigation per FR-UI-1. Arrow keys navigate: `↑`/`↓` move between cards within a column, `←`/`→` move focus between columns. `Enter` opens the detail panel for the focused card. `Space` toggles card selection for multi-select. The currently focused card MUST have a visible focus ring (using `--color-focus-ring`).

### 10.7 Multi-Select and Bulk Actions

**FR-UI-21l** The board MUST support the same multi-select behavior as the tree view (FR-UI-14). `Shift+Click` on cards enables range selection within a column. `Cmd+Click` toggles individual card selection across columns. Selected cards display a bulk action toolbar with the same actions available in tree view (done, priority, assign, rerun, cancel). A card MUST NOT be simultaneously selected-for-bulk-action and being dragged — drag initiation clears multi-select.

### 10.8 Layout Integration

**FR-UI-21m** The Kanban board replaces the **main content area** of the two-panel layout (FR-UI-2), NOT the entire layout. The collapsible sidebar tree remains visible and functional when Kanban is active — selecting a node in the sidebar scopes the board to that subtree (FR-UI-21h). The `ViewType` in NavigationContext MUST include `"kanban"` as a valid view option.

### 10.9 Column Ordering and Blocked Status

**FR-UI-21n** Column order is fixed and NOT user-customizable: **Open → In Progress → Blocked → Deferred → Done → Cancelled**. This reflects the typical workflow progression. WIP limits are not enforced in Phase 1.

**FR-UI-21o** Dragging a Blocked card to any column MUST be rejected — blocked status is auto-managed by the dependency system (FR-3.8). The board MUST show a popover: "Resolve blocking dependencies first." No card movement occurs. The card remains in the Blocked column.

---

## 11. Summary of New UI Requirements

| Requirement | Description |
|-------------|-------------|
| **FR-UI-1** | Linear-style SPA with keyboard-first navigation and sub-100ms perceived latency |
| **FR-UI-2** | Two-panel layout: collapsible sidebar tree + main content area (not three-panel) |
| **FR-UI-3** | Command palette (`Cmd+K`) with fuzzy search across nodes, actions, and navigation |
| **FR-UI-4** | Inline prompt editor with distinct "Human Addition" blocks |
| **FR-UI-5** | "Save & Rerun Children" action that cascades invalidation down the tree |
| **FR-UI-6** | Four rerun strategies: rerun all, rerun open only, delete & re-decompose, manual review |
| **FR-UI-7** | New node status `invalidated` with visual indicator and previous_status preservation |
| **FR-UI-8** | Prompt annotations (like PR comments) — attributed, timestamped, resolvable |
| **FR-UI-9** | Agent Dashboard view showing real-time agent state, current work, and activity timeline |
| **FR-UI-10** | Breadcrumb bar with progress summary always visible at bottom |
| **FR-UI-11** | Quick-add bar for fast micro issue creation with optional inline prompt |
| **FR-UI-12** | Diff view for prompt edits showing what changed |
| **FR-UI-13** | Inline status transitions on hover (single-click forward, popover for destructive) |
| **FR-UI-14** | Multi-select with bulk actions (done, priority, assign, rerun, cancel) |
| **FR-UI-15** | Compact/comfortable density toggle |
| **FR-UI-16** | Light/dark theme with system preference detection |
| **FR-UI-17** | Real-time updates via WebSocket — no manual refresh needed |
| **FR-UI-18** | CLI commands: `mtix prompt`, `mtix annotate`, `mtix rerun`, `mtix restore`, `mtix resolve-annotation` |
| **FR-UI-19** | REST endpoints for prompt editing, annotations, annotation resolution, rerun, restore, and undelete |
| **FR-UI-20** | WebSocket event types: node.created, node.updated, node.deleted, nodes.deleted, progress.changed, prompt.edited, prompt.annotated, nodes.invalidated, agent.state, agent.stuck, agent.heartbeat (11 types — see Section 7) |
| **FR-UI-21** | Kanban board view with status columns, drag-and-drop transitions, real-time updates, subtree scoping |
