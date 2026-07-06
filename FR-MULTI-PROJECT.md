# FR-MULTI-PROJECT: Multiple projects in one mtix database

- **Status:** Proposed
- **Supersedes:** REQUIREMENTS.md FR-7.3a (the "single-project-per-directory, multi-project deferred" note)
- **Audience:** the implementing agent/dev. This is the spec; the data layer already does most of the work (see §2).

## 1. Motivation

On large efforts, a team wants more than one project prefix tracked in the **same** mtix database — e.g. a primary `MTIX` project plus a `MTIX-DEV-OPS` project for ops tickets — so teammates can pick up devops work and house-keeping stays separated, without standing up a second `.mtix` directory or a second hub. The data model already supports coexisting prefixes; the product surface (CLI/API/MCP/UI) assumes one project per directory. This FR makes multi-project a first-class, opt-in experience while keeping single-project usage **byte-for-byte unchanged**.

## 2. Current state (feasibility summary)

The storage layer is already multi-project-capable:
- `nodes.id` is the primary key and embeds the prefix; `project` is a per-row column; there is **no** singleton/CHECK constraint binding a DB to one project. `MTIX-1` and `MTIX-DEV-OPS-1` coexist with no collision.
- ID parsing handles multi-hyphen prefixes: `splitID` cuts at the **last** dash before the first dot, so `MTIX-DEV-OPS-1.2.3` → prefix `MTIX-DEV-OPS`, root `1`, path `2.3`. The prefix regex `^[A-Z][A-Z0-9-]{0,19}$` allows internal hyphens.
- Sequence claiming is scoped per `(project, parent)`; each project numbers independently.
- The Postgres hub is namespaced by `project_prefix` (registry, version gate, dedup sweep, settlement are per-project).

The gaps are entirely in the product surface: the CLI is bound to one configured `prefix`, query commands/endpoints have no project filter, there is no project-discovery command, and the web UI is single-project.

## 3. Design decisions (locked)

- **D1 — Primary prefix + overrides.** The existing config `prefix` remains the **primary** project and the **default scope** everywhere. Other projects are addressed explicitly. Backward-compatible by construction.
- **D2 — Sync-all-projects.** Sync pushes/pulls **every** project in the local DB to the team hub (the hub is already per-project namespaced). No per-project cursors or isolation — that complexity adds no value for the team-hub use case.
- **D3 — Implicit project creation + guardrail.** A new project is created implicitly by creating a root in it; no separate "register project" step. Projects are **derived from data** (`SELECT DISTINCT project`), not a config list. Interactive surfaces (CLI/UI) **confirm on an unknown prefix** to prevent typo-projects.
- **D4 — Badge for visual distinction.** In all-projects views, ids carry a small project-prefix badge (not full row color-tint). Hidden in single/scoped views to avoid noise.
- **D5 — Allow create-new-project from the UI.** The web create form may introduce a new project (combobox + confirm), for surface parity with the CLI.

## 4. Terminology

- **Project prefix** — the leading id segment (`MTIX`, `MTIX-DEV-OPS`).
- **Primary project** — the config `prefix`; the default scope.
- **Active scope** — what a list-style view is currently showing: a single project (default: primary) or **all projects**.

## 5. Requirements

### 5.1 Model & config
- **MP-1** No schema change is required. `project` stays a per-node column; ids remain the source of truth for a node's project.
- **MP-2** Config keeps a single `prefix` = the primary project. No config list of projects; the project set is derived from node data.
- **MP-3** A store-level project filter MUST exist: add a `Project` field (and an "all projects" mode) to `store.NodeFilter`, honored by `ListNodes` and the query helpers. Empty/primary default preserves current behavior.
- **MP-4** A store method MUST return the distinct projects in the DB with per-project node counts and a primary flag (backing `mtix projects` and `GET /projects`).

### 5.2 CLI
- **MP-5** `mtix create --project <PREFIX>` sets the project for a **root** node, overriding the primary. For a **child** create, the project is **inherited from the parent** and `--project`, if given, MUST match the parent's project (else error) — a node can never be filed into a different project than its parent.
- **MP-6** If `--project` names a prefix not already present in the DB, the CLI MUST confirm (`Create new project <PREFIX>? [y/N]`) unless `--yes`. Invalid prefixes (failing the grammar) are rejected.
- **MP-7** The list-style commands — `list`, `search`, `query`, `orphans`, `blocked`, `ready`, `stale` — MUST accept `--project <PREFIX>` (scope to one) and `--all-projects` (span all). With neither, they default to the **primary** project. Id-addressed commands (`show <id>`, `tree <id>`) are unaffected (the id carries the project).
- **MP-8** `mtix projects` MUST list the distinct projects with node counts, marking the primary.

### 5.3 HTTP API
- **MP-9** The list-style endpoints (`/search`, `/orphans`, `/blocked`, `/stale`, `/stats`) MUST accept a `project` query param (default: primary) and a way to request all projects (`project=all` or `all_projects=true`).
- **MP-10** `GET /api/v1/projects` MUST return the project list (prefix, count, isPrimary).
- **MP-11** `POST /nodes` already accepts `project`; it MUST continue to (defaulting to primary when omitted). Non-interactive callers (API/MCP) may create into a new project directly without a confirmation prompt (the D3 guardrail is interactive-only).

### 5.4 MCP (agent tools)
- **MP-12** Query tools (`mtix_list`, `mtix_search`, `mtix_show`/briefing as applicable) MUST accept an optional `project` arg (default primary; `all` to span), for symmetry with `mtix_create` (which already takes `project`).
- **MP-13** `mtix_create`'s `project` SHOULD default to the primary when omitted (rather than hard-require it), so simple agents stay simple while multi-project agents stay explicit.

### 5.5 Web UI
- **MP-14** A `ProjectContext` MUST hold the active scope (a project prefix or "all"), defaulting to the primary, persisted to `localStorage`. It loads the project list from `GET /projects`.
- **MP-15** Wire the existing **TopBar "Select Project" placeholder** (`TopBar.tsx:46`) to a scope dropdown: primary (default), each other project, and "All projects." When only one project exists, the control is unobtrusive (shows the single project or hides).
- **MP-16** The active scope MUST thread through `useFilters` into all list-style views (All Issues, Kanban, Stale, Agents, Dashboard) and the sidebar tree, via the MP-9 `project` query param.
- **MP-17** `CreateNodeModal` MUST gain a project field: defaults to the active scope (primary in "all" mode); for a **child** it is inherited and locked to the parent's project; for a **root** it is a combobox allowing an existing pick or a new prefix → confirm dialog (D5) before submit.
- **MP-18** A shared `<NodeID>` component MUST render node ids consistently and, in **all-projects** scope, append a small project-prefix badge (D4). Single/scoped views render the id plainly.
- **MP-19** Live WebSocket updates MUST respect the active scope (a create in another project does not appear in a scoped view).

### 5.6 Sync
- **MP-20** Per D2, sync pushes/pulls all projects in the local DB; no per-project filtering or cursors are added. The behavior MUST be documented (USERMANUAL/SYNC-DESIGN) as intentional: one DB ↔ one hub carries all of that DB's projects.
- **MP-21** No change to the hub's existing per-project mechanics (registry, version gate, dedup sweep, settlement). The hub-global restore-epoch continues to apply uniformly across projects.

## 6. Backward compatibility

A single-project DB MUST behave exactly as today: the primary is the default scope, no command requires `--project`, no prompt appears, and the UI shows one project without added chrome. All multi-project affordances are opt-in and appear only when >1 project exists (or `--project`/`--all-projects` is used).

## 7. Out of scope (future)

- Per-project sync isolation / routing a project to a subset of clients (explicitly rejected for now, D2).
- Project rename / merge / archive / delete.
- Cross-project dependencies: technically permitted by the data model; this FR neither adds nor forbids them, but surfaces should render a cross-project dep clearly. A dedicated policy is future work.
- Per-project access control / permissions.

## 8. Acceptance criteria & test scenarios

- **AC-1** In one DB: create roots in `MTIX` and `MTIX-DEV-OPS` (CLI `--project` + the new-project confirm), create children in each; children inherit the parent's project; `--project` mismatching a parent errors.
- **AC-2** `mtix list` (no flag) shows only the primary; `--project MTIX-DEV-OPS` shows only that; `--all-projects` shows both. Same for `search`/`orphans`/etc.
- **AC-3** `mtix projects` lists both with correct counts and the primary marked.
- **AC-4** Multi-hyphen prefix `MTIX-DEV-OPS` round-trips through create → show → tree → renumber (atomic subtree renumber keeps the prefix intact) with no mis-parse.
- **AC-5** Sync: a DB holding both projects pushes both to the hub; a fresh clone `mtix sync` reconstructs both projects and converges.
- **AC-6** Web UI: the scope selector switches All Issues / Kanban / tree between primary, a named project, and all-projects; the `<NodeID>` badge appears only in all-projects; the create modal inherits a child's project and lets a root pick/add a project.
- **AC-7 (regression)** An existing single-project DB exercises every command/endpoint/MCP tool/UI view with identical behavior to pre-feature (no `--project` needed, no prompts, no UI chrome).

## 9. Supersedes

This document supersedes FR-7.3a. REQUIREMENTS.md FR-7.3a is updated to reference it: multi-project in a single DB is **supported** per this FR (no longer deferred).
