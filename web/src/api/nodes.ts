/**
 * Node API methods per FR-8.
 * Maps to the REST API endpoints exposed by mtix serve.
 */

import type { Node, Stats, ContextResponse, Annotation, ActivityEntry, Dependency } from "../types";
import { get, post, patch, del } from "./client";

/** List filter parameters. */
export interface ListOptions {
  status?: string;
  priority?: number;
  assignee?: string;
  under?: string;
  limit?: number;
  offset?: number;
}

/** Get a node by ID. */
export function getNode(id: string): Promise<Node> {
  return get<Node>(`/nodes/${encodeURIComponent(id)}`);
}

/** List/search nodes with optional filters via GET /search. */
export function listNodes(options?: ListOptions): Promise<{ nodes: Node[]; total: number; has_more: boolean }> {
  const params = new URLSearchParams();
  if (options?.status) params.set("status", options.status);
  if (options?.assignee) params.set("assignee", options.assignee);
  if (options?.under) params.set("under", options.under);
  if (options?.limit) params.set("limit", String(options.limit));
  if (options?.offset) params.set("offset", String(options.offset));

  const qs = params.toString();
  return get<{ nodes: Node[]; total: number; has_more: boolean }>(`/search${qs ? `?${qs}` : ""}`);
}

/** Get children of a node via GET /nodes/:id/children. */
export function getChildren(parentId: string): Promise<Node[]> {
  return get<{ children: Node[] }>(`/nodes/${encodeURIComponent(parentId)}/children`).then(
    (r) => r.children ?? [],
  );
}

/** Get root-level nodes via GET /orphans. */
export function getRootNodes(limit = 200): Promise<{ nodes: Node[]; total: number }> {
  const params = new URLSearchParams({ limit: String(limit) });
  return get<{ nodes: Node[]; total: number }>(`/orphans?${params.toString()}`);
}

/** Create a new node. */
export function createNode(
  node: Partial<Node> & { title: string; project?: string },
): Promise<Node> {
  return post<Node>("/nodes", node);
}

/** Update a node. */
export function updateNode(
  id: string,
  fields: Partial<Node>,
): Promise<Node> {
  return patch<Node>(`/nodes/${encodeURIComponent(id)}`, fields);
}

/** Delete (soft-delete) a node. */
export function deleteNode(id: string): Promise<void> {
  return del<void>(`/nodes/${encodeURIComponent(id)}`);
}

/** Search nodes via GET /search with filters. */
export function searchNodes(
  query: string,
  options?: { limit?: number; offset?: number },
): Promise<Node[]> {
  // Use the search endpoint with under filter for prefix matching.
  const params = new URLSearchParams();
  if (query) params.set("under", query);
  if (options?.limit) params.set("limit", String(options.limit));
  if (options?.offset) params.set("offset", String(options.offset));
  return get<{ nodes: Node[] }>(`/search?${params.toString()}`).then((r) => r.nodes ?? []);
}

/** Get node tree. */
export function getTree(
  rootId: string,
  maxDepth?: number,
): Promise<Node[]> {
  const params = new URLSearchParams();
  if (maxDepth !== undefined) params.set("depth", String(maxDepth));
  const qs = params.toString();
  return get<Node[]>(
    `/nodes/${encodeURIComponent(rootId)}/tree${qs ? `?${qs}` : ""}`,
  );
}

/** Get aggregate stats. */
export function getStats(scopeId?: string): Promise<Stats> {
  const params = new URLSearchParams();
  if (scopeId) params.set("scope", scopeId);
  const qs = params.toString();
  return get<Stats>(`/stats${qs ? `?${qs}` : ""}`);
}

/** Get context for a node via GET /context/:id. */
export function getContext(id: string): Promise<ContextResponse> {
  return get<ContextResponse>(
    `/context/${encodeURIComponent(id)}`,
  );
}

/** Claim a node. */
export function claimNode(
  id: string,
  agent: string,
): Promise<Node> {
  return post<Node>(`/nodes/${encodeURIComponent(id)}/claim`, { agent });
}

/** Transition node status. */
export function transitionNode(
  id: string,
  action: string,
  body?: Record<string, unknown>,
): Promise<void> {
  return post<void>(`/nodes/${encodeURIComponent(id)}/${action}`, body ?? {});
}

/** Update node prompt per FR-UI-4. */
export function updatePrompt(
  id: string,
  prompt: string,
): Promise<Node> {
  return patch<Node>(
    `/nodes/${encodeURIComponent(id)}`,
    { prompt },
  );
}

/** Add annotation to node via comment endpoint. */
export function addAnnotation(
  id: string,
  text: string,
): Promise<Annotation> {
  return post<Annotation>(
    `/nodes/${encodeURIComponent(id)}/comment`,
    { text },
  );
}

/** Resolve or unresolve an annotation. */
export function resolveAnnotation(
  nodeId: string,
  annotationId: string,
  resolved: boolean,
): Promise<Annotation> {
  return patch<Annotation>(
    `/nodes/${encodeURIComponent(nodeId)}`,
    { annotation_id: annotationId, resolved },
  );
}

/** Rerun strategy per FR-UI-6. */
export type RerunStrategy = "all" | "open_only" | "delete" | "review";

/** Trigger rerun of children per FR-UI-5. */
export function rerunChildren(
  id: string,
  strategy: RerunStrategy,
): Promise<void> {
  return post<void>(
    `/nodes/${encodeURIComponent(id)}/rerun`,
    { strategy },
  );
}

/** Get activity log for a node per FR-3.6. */
export async function getActivity(
  id: string,
  options?: { limit?: number; offset?: number },
): Promise<ActivityEntry[]> {
  const params = new URLSearchParams();
  if (options?.limit) params.set("limit", String(options.limit));
  if (options?.offset) params.set("offset", String(options.offset));
  const qs = params.toString();
  const resp = await get<{ entries: ActivityEntry[]; total: number }>(
    `/nodes/${encodeURIComponent(id)}/activity${qs ? `?${qs}` : ""}`,
  );
  return resp.entries ?? [];
}

/** Get dependencies for a node via GET /deps/:id. */
export function getDependencies(
  id: string,
): Promise<Dependency[]> {
  return get<{ blockers: Dependency[]; total: number }>(
    `/deps/${encodeURIComponent(id)}`,
  ).then((r) => r.blockers ?? []);
}

/** Get stale entries via GET /stale. Returns stale agent IDs. */
export function getStaleEntries(
  hours?: number,
): Promise<{ agents: string[]; total: number }> {
  const params = new URLSearchParams();
  if (hours) params.set("hours", String(hours));
  const qs = params.toString();
  return get<{ agents: string[]; total: number }>(`/stale${qs ? `?${qs}` : ""}`);
}

/** Get ready nodes. */
export function getReadyNodes(): Promise<Node[]> {
  return get<{ nodes: Node[] }>("/ready").then((r) => r.nodes ?? []);
}

/** Get blocked nodes. */
export function getBlockedNodes(): Promise<Node[]> {
  return get<{ nodes: Node[] }>("/blocked").then((r) => r.nodes ?? []);
}
