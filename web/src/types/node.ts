/**
 * Core domain types for mtix, mirroring the Go model package.
 * All types match the REST API JSON format per FR-8.
 */

/** Node lifecycle state per FR-3.5. */
export type Status =
  | "open"
  | "in_progress"
  | "blocked"
  | "done"
  | "deferred"
  | "cancelled"
  | "invalidated";

/** Tier classification per FR-1.2. */
export type NodeType = "story" | "epic" | "issue" | "micro" | "auto";

/** Nature of work classification. */
export type IssueType =
  | "bug"
  | "feature"
  | "task"
  | "chore"
  | "refactor"
  | "test"
  | "doc";

/** Priority levels matching Go model.Priority constants. */
export type Priority = 1 | 2 | 3 | 4 | 5;

/** Priority labels for display. */
export const PriorityLabel: Record<Priority, string> = {
  1: "Critical",
  2: "High",
  3: "Medium",
  4: "Low",
  5: "Background",
};

/** Dependency relationship type per FR-4.2. */
export type DepType = "blocks" | "related" | "discovered_from" | "duplicates";

/** LLM agent state. */
export type AgentState = "idle" | "working" | "stuck" | "done";

/** Reference to source code location. */
export interface CodeRef {
  file: string;
  line: number;
  symbol: string;
}

/** Prompt annotation per FR-12.2. */
export interface Annotation {
  id: string;
  author: string;
  text: string;
  created_at: string;
  resolved: boolean;
}

/** Activity log entry per FR-3.6. */
export interface ActivityEntry {
  id: string;
  type: string;
  author: string;
  text: string;
  created_at: string;
}

/** A mtix node per FR-1. */
export interface Node {
  id: string;
  parent_id: string;
  project: string;
  depth: number;
  seq: number;
  title: string;
  description: string;
  prompt: string;
  acceptance: string;
  labels: string[];
  priority: Priority;
  status: Status;
  node_type: NodeType;
  issue_type: IssueType;
  creator: string;
  assignee: string;
  agent_state: AgentState;
  weight: number;
  progress: number;
  content_hash: string;
  child_count: number;
  created_at: string | null;
  updated_at: string | null;
  closed_at: string | null;
  defer_until: string | null;
  deleted_at: string | null;
  code_refs?: CodeRef[];
  annotations?: Annotation[];
  activity?: ActivityEntry[];
}

/** Dependency between nodes per FR-4. */
export interface Dependency {
  from_id: string;
  to_id: string;
  dep_type: DepType;
  created_at: string;
}

/** Assembled context for a node per FR-12.1. */
export interface ContextResponse {
  chain: ContextEntry[];
  siblings: ContextSibling[];
  blocking_deps: Dependency[];
  assembled_prompt: string;
}

/** A single node in the context chain. */
export interface ContextEntry {
  id: string;
  title: string;
  status: Status;
  prompt: string;
  acceptance: string;
  depth: number;
}

/** A sibling node in context. */
export interface ContextSibling {
  id: string;
  title: string;
  status: Status;
}

/** Progress rollup for a node per FR-5. */
export interface ProgressResponse {
  node_id: string;
  progress: number;
  total_children: number;
  done_children: number;
  open_children: number;
  blocked_children: number;
}

/** Agent session info for agent dashboard per FR-UI-9. */
export interface AgentInfo {
  agent_id: string;
  state: AgentState;
  current_node_id: string | null;
  current_node_title: string | null;
  session_started_at: string | null;
  last_heartbeat: string | null;
  nodes_completed: number;
  recent_actions: ActivityEntry[];
}

/** Stale node entry for stale board per MTIX-9.4.4. */
export interface StaleEntry {
  node: Node;
  reason: "in_progress_too_long" | "stale_heartbeat" | "invalidated" | "blocked_not_unblocked";
  time_since_activity: number;
  assigned_agent: string | null;
}

/** Aggregate statistics per FR-2.7. */
export interface Stats {
  total_nodes: number;
  by_status: Record<string, number>;
  by_priority: Record<string, number>;
  by_type: Record<string, number>;
  progress: number;
  scope_id: string;
}
