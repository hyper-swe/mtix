/**
 * StaleBoard — shows nodes that need attention.
 * Per MTIX-9.4.4: nodes in_progress too long, stale heartbeats,
 * invalidated awaiting review, and blocked but not auto-unblocked.
 */

import { useMemo } from "react";
import type { StaleEntry } from "../types";
import { StatusIcon } from "./StatusIcon";

/** Human-readable staleness reason. */
function reasonLabel(
  reason: StaleEntry["reason"],
): string {
  switch (reason) {
    case "in_progress_too_long":
      return "In progress too long";
    case "stale_heartbeat":
      return "Agent heartbeat stale";
    case "invalidated":
      return "Awaiting review";
    case "blocked_not_unblocked":
      return "Blocked (should auto-unblock)";
    default:
      return "Needs attention";
  }
}

/** Reason badge color. */
function reasonColor(reason: StaleEntry["reason"]): string {
  switch (reason) {
    case "in_progress_too_long":
      return "var(--color-status-in-progress)";
    case "stale_heartbeat":
      return "var(--color-status-blocked)";
    case "invalidated":
      return "var(--color-status-invalidated)";
    case "blocked_not_unblocked":
      return "var(--color-status-blocked)";
    default:
      return "var(--color-border)";
  }
}

/** Format seconds to human-readable duration. */
function formatStaleDuration(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}

export type StaleAction = "reassign" | "cancel" | "rerun" | "unblock";

export interface StaleBoardProps {
  /** Stale entries to display. */
  entries: StaleEntry[];
  /** Quick action callback. */
  onAction: (nodeId: string, action: StaleAction) => void;
  /** Navigate to a node. */
  onNavigate: (nodeId: string) => void;
  /** Additional CSS class. */
  className?: string;
}

export function StaleBoard({
  entries,
  onAction,
  onNavigate,
  className = "",
}: StaleBoardProps) {
  // Group by reason category.
  const grouped = useMemo(() => {
    const groups = new Map<StaleEntry["reason"], StaleEntry[]>();
    for (const entry of entries) {
      const existing = groups.get(entry.reason) ?? [];
      existing.push(entry);
      groups.set(entry.reason, existing);
    }
    return groups;
  }, [entries]);

  if (entries.length === 0) {
    return (
      <div
        className={`flex flex-col items-center justify-center p-8 ${className}`}
        data-testid="stale-board"
      >
        <p
          className="text-sm"
          style={{ color: "var(--color-text-secondary)" }}
        >
          No stale items
        </p>
        <p
          className="text-xs mt-1"
          style={{ color: "var(--color-text-secondary)" }}
        >
          Everything is running smoothly
        </p>
      </div>
    );
  }

  const categories: StaleEntry["reason"][] = [
    "stale_heartbeat",
    "in_progress_too_long",
    "invalidated",
    "blocked_not_unblocked",
  ];

  return (
    <div
      className={`flex flex-col gap-4 p-4 overflow-y-auto ${className}`}
      data-testid="stale-board"
    >
      <h2
        className="text-lg font-medium"
        style={{ color: "var(--color-text-primary)" }}
      >
        Stale Board
      </h2>

      <p
        className="text-xs"
        style={{ color: "var(--color-text-secondary)" }}
      >
        {entries.length} item{entries.length !== 1 ? "s" : ""} need
        attention
      </p>

      {categories.map((reason) => {
        const items = grouped.get(reason);
        if (!items || items.length === 0) return null;

        return (
          <div
            key={reason}
            className="rounded border"
            style={{
              borderColor: "var(--color-border)",
              backgroundColor: "var(--color-surface)",
            }}
            data-testid={`stale-category-${reason}`}
          >
            {/* Category header */}
            <div
              className="px-3 py-2 text-xs font-medium border-b flex items-center gap-2"
              style={{
                borderColor: "var(--color-border)",
                color: reasonColor(reason),
              }}
            >
              <span
                className="w-2 h-2 rounded-full"
                style={{ backgroundColor: reasonColor(reason) }}
              />
              {reasonLabel(reason)} ({items.length})
            </div>

            {/* Items */}
            {items.map((entry) => (
              <div
                key={entry.node.id}
                className="flex items-center gap-3 px-3 py-2 border-b last:border-b-0"
                style={{ borderColor: "var(--color-border)" }}
                data-testid={`stale-item-${entry.node.id}`}
              >
                <StatusIcon status={entry.node.status} size={14} />

                <button
                  className="text-xs font-mono shrink-0 cursor-pointer hover:underline"
                  style={{ color: "var(--color-accent)" }}
                  onClick={() => onNavigate(entry.node.id)}
                  data-testid={`stale-link-${entry.node.id}`}
                >
                  {entry.node.id}
                </button>

                <span
                  className="text-sm flex-1 truncate"
                  style={{ color: "var(--color-text-primary)" }}
                >
                  {entry.node.title}
                </span>

                <span
                  className="text-xs tabular-nums shrink-0"
                  style={{ color: "var(--color-text-secondary)" }}
                  data-testid="stale-duration"
                >
                  {formatStaleDuration(entry.time_since_activity)}
                </span>

                {entry.assigned_agent && (
                  <span
                    className="text-xs shrink-0"
                    style={{ color: "var(--color-accent)" }}
                  >
                    {entry.assigned_agent}
                  </span>
                )}

                {/* Quick actions */}
                <div className="flex gap-1 shrink-0">
                  {entry.reason === "blocked_not_unblocked" && (
                    <button
                      className="text-xs px-1.5 py-0.5 rounded cursor-pointer"
                      style={{
                        backgroundColor: "var(--color-status-done)",
                        color: "#FFFFFF",
                      }}
                      onClick={() => onAction(entry.node.id, "unblock")}
                      data-testid={`action-unblock-${entry.node.id}`}
                    >
                      Unblock
                    </button>
                  )}
                  <button
                    className="text-xs px-1.5 py-0.5 rounded cursor-pointer"
                    style={{
                      backgroundColor: "var(--color-accent)",
                      color: "#FFFFFF",
                    }}
                    onClick={() => onAction(entry.node.id, "reassign")}
                    data-testid={`action-reassign-${entry.node.id}`}
                  >
                    Reassign
                  </button>
                  <button
                    className="text-xs px-1.5 py-0.5 rounded cursor-pointer"
                    style={{
                      color: "var(--color-text-secondary)",
                      border: "1px solid var(--color-border)",
                    }}
                    onClick={() => onAction(entry.node.id, "cancel")}
                    data-testid={`action-cancel-${entry.node.id}`}
                  >
                    Cancel
                  </button>
                </div>
              </div>
            ))}
          </div>
        );
      })}
    </div>
  );
}
