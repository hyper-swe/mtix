/**
 * Dashboard — project-wide progress dashboard.
 * Shows overall progress, status breakdown, per-story progress,
 * agent productivity, and time-series chart.
 * Per MTIX-9.4.1 and FR-UI-9.
 */

import { useState, useMemo } from "react";
import type { Stats } from "../types";
import { ProgressBar } from "./ProgressBar";
import { StatusIcon } from "./StatusIcon";
import type { Status } from "../types";

/** Time range for burndown/productivity charts. */
export type TimeRange = "7d" | "30d" | "90d";

/** Status colors for the stacked bar chart. */
const STATUS_COLORS: Record<string, string> = {
  open: "var(--color-status-open)",
  in_progress: "var(--color-status-in-progress)",
  blocked: "var(--color-status-blocked)",
  done: "var(--color-status-done)",
  deferred: "var(--color-status-deferred)",
  cancelled: "var(--color-status-cancelled)",
  invalidated: "var(--color-status-invalidated)",
};

/** Story progress entry. */
export interface StoryProgress {
  id: string;
  title: string;
  progress: number;
  status: Status;
  childCount: number;
}

/** Agent productivity entry. */
export interface AgentProductivity {
  agentId: string;
  completed: number;
  inProgress: number;
}

/** Burndown data point. */
export interface BurndownPoint {
  date: string;
  completed: number;
  total: number;
}

export interface DashboardProps {
  /** Project-wide stats. */
  stats: Stats;
  /** Per-story progress data. */
  stories: StoryProgress[];
  /** Agent productivity data. */
  agentStats: AgentProductivity[];
  /** Burndown chart data. */
  burndown: BurndownPoint[];
  /** Navigate to a node. */
  onNavigate: (nodeId: string) => void;
  /** Additional CSS class. */
  className?: string;
}

export function Dashboard({
  stats,
  stories,
  agentStats,
  burndown,
  onNavigate,
  className = "",
}: DashboardProps) {
  const [timeRange, setTimeRange] = useState<TimeRange>("30d");

  const totalNodes = stats.total_nodes || 1;

  const statusEntries = useMemo(() => {
    return Object.entries(stats.by_status).sort(
      ([, a], [, b]) => b - a,
    );
  }, [stats.by_status]);

  const filteredBurndown = useMemo(() => {
    const days = timeRange === "7d" ? 7 : timeRange === "30d" ? 30 : 90;
    return burndown.slice(-days);
  }, [burndown, timeRange]);

  const timeRanges: { label: string; value: TimeRange }[] = [
    { label: "7 days", value: "7d" },
    { label: "30 days", value: "30d" },
    { label: "90 days", value: "90d" },
  ];

  return (
    <div
      className={`flex flex-col gap-6 p-4 overflow-y-auto ${className}`}
      data-testid="dashboard"
    >
      {/* Header */}
      <h2
        className="text-lg font-medium"
        style={{ color: "var(--color-text-primary)" }}
      >
        Progress Dashboard
      </h2>

      {/* Overall progress */}
      <div
        className="rounded border p-4"
        style={{
          borderColor: "var(--color-border)",
          backgroundColor: "var(--color-surface)",
        }}
        data-testid="overall-progress"
      >
        <h3
          className="text-sm font-medium mb-2"
          style={{ color: "var(--color-text-secondary)" }}
        >
          Overall Progress
        </h3>
        <ProgressBar progress={stats.progress} showLabel height={10} />
        <p
          className="text-xs mt-2"
          style={{ color: "var(--color-text-secondary)" }}
        >
          {stats.total_nodes} nodes total
        </p>
      </div>

      {/* Status breakdown — stacked bar */}
      <div
        className="rounded border p-4"
        style={{
          borderColor: "var(--color-border)",
          backgroundColor: "var(--color-surface)",
        }}
        data-testid="status-breakdown"
      >
        <h3
          className="text-sm font-medium mb-3"
          style={{ color: "var(--color-text-secondary)" }}
        >
          Status Breakdown
        </h3>

        {/* Stacked bar */}
        <div
          className="h-6 rounded-full overflow-hidden flex"
          style={{ backgroundColor: "var(--color-border)" }}
          data-testid="stacked-bar"
        >
          {statusEntries.map(([status, count]) => {
            const pct = (count / totalNodes) * 100;
            if (pct <= 0) return null;
            return (
              <div
                key={status}
                style={{
                  width: `${pct}%`,
                  backgroundColor: STATUS_COLORS[status] ?? "var(--color-border)",
                  transition: "width 300ms ease",
                }}
                title={`${status}: ${count} (${Math.round(pct)}%)`}
                data-testid={`bar-${status}`}
              />
            );
          })}
        </div>

        {/* Legend */}
        <div className="flex flex-wrap gap-3 mt-3">
          {statusEntries.map(([status, count]) => (
            <div
              key={status}
              className="flex items-center gap-1.5 text-xs"
              data-testid={`legend-${status}`}
            >
              <StatusIcon status={status as Status} size={12} />
              <span style={{ color: "var(--color-text-secondary)" }}>
                {status}: {count}
              </span>
            </div>
          ))}
        </div>
      </div>

      {/* Time range selector + Burndown chart */}
      <div
        className="rounded border p-4"
        style={{
          borderColor: "var(--color-border)",
          backgroundColor: "var(--color-surface)",
        }}
        data-testid="burndown-section"
      >
        <div className="flex items-center justify-between mb-3">
          <h3
            className="text-sm font-medium"
            style={{ color: "var(--color-text-secondary)" }}
          >
            Completion Trend
          </h3>
          <div className="flex gap-1">
            {timeRanges.map((tr) => (
              <button
                key={tr.value}
                className="px-2 py-0.5 text-xs rounded cursor-pointer"
                style={{
                  backgroundColor:
                    timeRange === tr.value
                      ? "var(--color-accent)"
                      : "transparent",
                  color:
                    timeRange === tr.value
                      ? "#FFFFFF"
                      : "var(--color-text-secondary)",
                }}
                onClick={() => setTimeRange(tr.value)}
                data-testid={`range-${tr.value}`}
              >
                {tr.label}
              </button>
            ))}
          </div>
        </div>

        {/* Simple bar chart for burndown */}
        <div
          className="flex items-end gap-0.5 h-32"
          data-testid="burndown-chart"
        >
          {filteredBurndown.length === 0 ? (
            <p
              className="text-xs w-full text-center self-center"
              style={{ color: "var(--color-text-secondary)" }}
            >
              No data for this period
            </p>
          ) : (
            filteredBurndown.map((point, i) => {
              const maxTotal = Math.max(
                ...filteredBurndown.map((p) => p.total || 1),
              );
              const height = (point.completed / maxTotal) * 100;
              return (
                <div
                  key={i}
                  className="flex-1 rounded-t"
                  style={{
                    height: `${Math.max(height, 2)}%`,
                    backgroundColor: "var(--color-status-done)",
                    opacity: 0.7 + (i / filteredBurndown.length) * 0.3,
                  }}
                  title={`${point.date}: ${point.completed}/${point.total}`}
                  data-testid="burndown-bar"
                />
              );
            })
          )}
        </div>
      </div>

      {/* Per-story progress */}
      <div
        className="rounded border p-4"
        style={{
          borderColor: "var(--color-border)",
          backgroundColor: "var(--color-surface)",
        }}
        data-testid="story-progress"
      >
        <h3
          className="text-sm font-medium mb-3"
          style={{ color: "var(--color-text-secondary)" }}
        >
          Stories
        </h3>
        {stories.length === 0 ? (
          <p
            className="text-xs text-center py-2"
            style={{ color: "var(--color-text-secondary)" }}
          >
            No stories
          </p>
        ) : (
          <div className="space-y-2">
            {stories.map((story) => (
              <div
                key={story.id}
                className="flex items-center gap-3 cursor-pointer"
                onClick={() => onNavigate(story.id)}
                data-testid={`story-${story.id}`}
              >
                <StatusIcon status={story.status} size={14} />
                <span
                  className="text-sm flex-1 truncate"
                  style={{ color: "var(--color-text-primary)" }}
                >
                  {story.title}
                </span>
                <div className="w-24">
                  <ProgressBar progress={story.progress} height={4} />
                </div>
                <span
                  className="text-xs tabular-nums shrink-0"
                  style={{ color: "var(--color-text-secondary)" }}
                >
                  {Math.round(story.progress * 100)}%
                </span>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Agent productivity */}
      <div
        className="rounded border p-4"
        style={{
          borderColor: "var(--color-border)",
          backgroundColor: "var(--color-surface)",
        }}
        data-testid="agent-productivity"
      >
        <h3
          className="text-sm font-medium mb-3"
          style={{ color: "var(--color-text-secondary)" }}
        >
          Agent Productivity
        </h3>
        {agentStats.length === 0 ? (
          <p
            className="text-xs text-center py-2"
            style={{ color: "var(--color-text-secondary)" }}
          >
            No agent data
          </p>
        ) : (
          <div className="space-y-2">
            {agentStats.map((agent) => (
              <div
                key={agent.agentId}
                className="flex items-center gap-3"
                data-testid={`agent-stat-${agent.agentId}`}
              >
                <span
                  className="text-sm flex-1"
                  style={{ color: "var(--color-accent)" }}
                >
                  {agent.agentId}
                </span>
                <span
                  className="text-xs tabular-nums"
                  style={{ color: "var(--color-status-done)" }}
                >
                  {agent.completed} done
                </span>
                <span
                  className="text-xs tabular-nums"
                  style={{ color: "var(--color-status-in-progress)" }}
                >
                  {agent.inProgress} active
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
