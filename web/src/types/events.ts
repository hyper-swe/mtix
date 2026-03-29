/**
 * WebSocket event types per FR-7.5a and requirement-ui.md §7.
 * All 11 event types that the UI subscribes to.
 */

import type { Node } from "./node";

/** All supported WebSocket event types. */
export type EventType =
  | "node.created"
  | "node.updated"
  | "node.deleted"
  | "nodes.deleted"
  | "node.undeleted"
  | "node.claimed"
  | "status.changed"
  | "progress.changed"
  | "prompt.edited"
  | "prompt.annotated"
  | "nodes.invalidated"
  | "dependency.added"
  | "dependency.removed"
  | "session.started"
  | "session.ended"
  | "annotation.added"
  | "config.changed"
  | "agent.state"
  | "agent.stuck"
  | "agent.heartbeat";

/** Base event structure. */
export interface BaseEvent {
  type: EventType;
  node_id?: string;
  timestamp?: string;
  author?: string;
}

/** Node created event. */
export interface NodeCreatedEvent extends BaseEvent {
  type: "node.created";
  node: Node;
  parent_id: string;
}

/** Node updated event. */
export interface NodeUpdatedEvent extends BaseEvent {
  type: "node.updated";
  node_id: string;
  fields: Partial<Node>;
}

/** Node deleted event. */
export interface NodeDeletedEvent extends BaseEvent {
  type: "node.deleted";
  node_id: string;
  deleted_by?: string;
}

/** Batch nodes deleted event. */
export interface NodesDeletedEvent extends BaseEvent {
  type: "nodes.deleted";
  parent_id: string;
  count: number;
  cascade: boolean;
}

/** Progress changed event. */
export interface ProgressChangedEvent extends BaseEvent {
  type: "progress.changed";
  node_id: string;
  progress: number;
}

/** Nodes invalidated event. */
export interface NodesInvalidatedEvent extends BaseEvent {
  type: "nodes.invalidated";
  parent_id: string;
  count: number;
  strategy: string;
}

/** Agent state change event. */
export interface AgentStateEvent extends BaseEvent {
  type: "agent.state";
  agent_id: string;
  state: string;
  node_id: string;
}

/** Agent stuck event. */
export interface AgentStuckEvent extends BaseEvent {
  type: "agent.stuck";
  agent_id: string;
  node_id: string;
}

/** Agent heartbeat event. */
export interface AgentHeartbeatEvent extends BaseEvent {
  type: "agent.heartbeat";
  agent_id: string;
  timestamp: string;
}

/** Union type of all WebSocket events. */
export type WebSocketEvent =
  | NodeCreatedEvent
  | NodeUpdatedEvent
  | NodeDeletedEvent
  | NodesDeletedEvent
  | ProgressChangedEvent
  | NodesInvalidatedEvent
  | AgentStateEvent
  | AgentStuckEvent
  | AgentHeartbeatEvent
  | BaseEvent;
