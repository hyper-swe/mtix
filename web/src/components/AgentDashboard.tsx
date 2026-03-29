/**
 * AgentDashboard — real-time view of all LLM agent activity.
 * Per FR-UI-9 and requirement-ui.md § 2 View B.
 * Shows agent cards sorted: working → stuck → idle.
 */

import { useMemo } from "react";
import type { AgentInfo } from "../types";

/** Format a duration in milliseconds to human-readable. */
function formatDuration(ms: number): string {
  const minutes = Math.floor(ms / 60000);
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  return `${hours}h ${remainingMinutes}m`;
}

/** Format relative time from ISO string. */
function timeAgo(isoString: string | null): string {
  if (!isoString) return "never";
  const diffMs = Date.now() - new Date(isoString).getTime();
  if (diffMs < 60000) return `${Math.floor(diffMs / 1000)}s ago`;
  if (diffMs < 3600000) return `${Math.floor(diffMs / 60000)} min ago`;
  return `${Math.floor(diffMs / 3600000)}h ago`;
}

/** Check if heartbeat is stale (>5 minutes per FR-10.3). */
function isHeartbeatStale(lastHeartbeat: string | null): boolean {
  if (!lastHeartbeat) return true;
  return Date.now() - new Date(lastHeartbeat).getTime() > 300000;
}

/** State sort order for agents: working first, then stuck, then idle. */
function stateSortKey(state: string): number {
  if (state === "working") return 0;
  if (state === "stuck") return 1;
  return 2;
}

export interface AgentDashboardProps {
  /** All registered agents. */
  agents: AgentInfo[];
  /** Navigate to a node when clicked. */
  onNavigate: (nodeId: string) => void;
  /** Additional CSS class. */
  className?: string;
}

export function AgentDashboard({
  agents,
  onNavigate,
  className = "",
}: AgentDashboardProps) {
  const sortedAgents = useMemo(() => {
    return [...agents].sort(
      (a, b) => stateSortKey(a.state) - stateSortKey(b.state),
    );
  }, [agents]);

  if (agents.length === 0) {
    return (
      <div
        className={`p-8 text-center ${className}`}
        data-testid="agent-dashboard"
      >
        <p style={{ color: "var(--color-text-secondary)" }}>
          No agents registered
        </p>
      </div>
    );
  }

  return (
    <div
      className={`flex flex-col gap-4 p-4 overflow-y-auto ${className}`}
      data-testid="agent-dashboard"
    >
      <h2
        className="text-lg font-medium"
        style={{ color: "var(--color-text-primary)" }}
      >
        Agent Activity
      </h2>

      {sortedAgents.map((agent) => (
        <AgentCard
          key={agent.agent_id}
          agent={agent}
          onNavigate={onNavigate}
        />
      ))}
    </div>
  );
}

interface AgentCardProps {
  agent: AgentInfo;
  onNavigate: (nodeId: string) => void;
}

function AgentCard({ agent, onNavigate }: AgentCardProps) {
  const isStuck = agent.state === "stuck";
  const isIdle = agent.state === "idle" || agent.state === "done";
  const staleHeart = isHeartbeatStale(agent.last_heartbeat);

  const sessionDuration = agent.session_started_at
    ? formatDuration(Date.now() - new Date(agent.session_started_at).getTime())
    : "—";

  return (
    <div
      className="rounded border p-4"
      style={{
        borderColor: isStuck
          ? "var(--color-status-blocked)"
          : "var(--color-border)",
        backgroundColor: "var(--color-surface)",
        opacity: isIdle ? 0.6 : 1,
        borderWidth: isStuck ? "2px" : "1px",
      }}
      data-testid={`agent-card-${agent.agent_id}`}
    >
      {/* Header row */}
      <div className="flex items-center justify-between mb-2">
        <div className="flex items-center gap-2">
          <span
            className="text-sm font-medium"
            style={{ color: "var(--color-accent)" }}
            data-testid="agent-name"
          >
            {agent.agent_id}
          </span>
          <span
            className="text-xs px-1.5 py-0.5 rounded font-bold uppercase"
            style={{
              backgroundColor:
                agent.state === "working"
                  ? "var(--color-status-in-progress)"
                  : agent.state === "stuck"
                    ? "var(--color-status-blocked)"
                    : "var(--color-border)",
              color: "#FFFFFF",
            }}
            data-testid="agent-status"
          >
            {agent.state}
          </span>
          {isStuck && (
            <span
              className="text-xs px-1 py-0.5 rounded"
              style={{
                backgroundColor: "var(--color-status-blocked)",
                color: "#FFFFFF",
              }}
              data-testid="stuck-badge"
            >
              STUCK
            </span>
          )}
        </div>

        <div
          className="flex items-center gap-3 text-xs"
          style={{ color: "var(--color-text-secondary)" }}
        >
          <span>Session: {sessionDuration}</span>
          <span
            style={{
              color: staleHeart
                ? "var(--color-status-blocked)"
                : "var(--color-text-secondary)",
            }}
            data-testid="heartbeat"
          >
            Heartbeat: {timeAgo(agent.last_heartbeat)}
          </span>
        </div>
      </div>

      {/* Current work */}
      {agent.current_node_id && !isIdle ? (
        <div className="mb-2">
          <div className="flex items-center gap-2">
            <span
              className="text-xs"
              style={{ color: "var(--color-text-secondary)" }}
            >
              Working on:
            </span>
            <button
              className="text-sm cursor-pointer hover:underline"
              style={{ color: "var(--color-text-primary)" }}
              onClick={() =>
                agent.current_node_id && onNavigate(agent.current_node_id)
              }
              data-testid="current-work"
            >
              {agent.current_node_id}{" "}
              {agent.current_node_title
                ? `"${agent.current_node_title}"`
                : ""}
            </button>
          </div>
        </div>
      ) : isIdle ? (
        <p
          className="text-xs mb-2"
          style={{ color: "var(--color-text-secondary)" }}
          data-testid="idle-message"
        >
          No active work.{" "}
          {agent.nodes_completed > 0
            ? `Completed ${agent.nodes_completed} nodes.`
            : ""}
        </p>
      ) : null}

      {/* Recent actions */}
      {agent.recent_actions.length > 0 && (
        <div className="mt-2">
          <span
            className="text-xs font-medium"
            style={{ color: "var(--color-text-secondary)" }}
          >
            Recent:
          </span>
          <div className="mt-1 space-y-0.5">
            {agent.recent_actions.slice(0, 5).map((action) => (
              <div
                key={action.id}
                className="flex items-center gap-2 text-xs"
                data-testid="agent-action"
              >
                <span
                  className="shrink-0 tabular-nums"
                  style={{ color: "var(--color-text-secondary)" }}
                >
                  {new Date(action.created_at).toLocaleTimeString([], {
                    hour: "2-digit",
                    minute: "2-digit",
                  })}
                </span>
                <span style={{ color: "var(--color-text-primary)" }}>
                  {action.text}
                </span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
