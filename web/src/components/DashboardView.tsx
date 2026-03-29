/**
 * Dashboard view — fetches stats and renders the Dashboard component.
 */

import { useEffect, useState } from "react";
import * as api from "../api";
import { ProgressBar } from "./ProgressBar";
import { StatusIcon } from "./StatusIcon";
import type { Status } from "../types";

interface DashboardViewProps {
  onNavigate: (nodeId: string) => void;
}

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

export function DashboardView({ onNavigate: _onNavigate }: DashboardViewProps) {
  const [stats, setStats] = useState<{ total: number; counts: Record<string, number> } | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setLoading(true);
    api.get<{ total: number; counts: Record<string, number> }>("/stats")
      .then((data) => {
        setStats(data);
        setError(null);
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : "Failed to load stats");
      })
      .finally(() => setLoading(false));
  }, []);

  if (loading) {
    return (
      <div
        className="flex items-center justify-center h-full"
        style={{ color: "var(--color-text-tertiary)" }}
      >
        <span className="text-sm">Loading dashboard...</span>
      </div>
    );
  }

  if (error || !stats) {
    return (
      <div className="flex items-center justify-center h-full">
        <span className="text-sm" style={{ color: "var(--color-status-blocked)" }}>
          {error ?? "No data"}
        </span>
      </div>
    );
  }

  const totalNodes = stats.total || 1;
  const doneCount = stats.counts["done"] ?? 0;
  const progress = totalNodes > 0 ? doneCount / totalNodes : 0;
  const statusEntries = Object.entries(stats.counts).sort(([, a], [, b]) => b - a);

  return (
    <div className="flex flex-col gap-5 p-6 overflow-y-auto h-full max-w-3xl">
      <h2
        className="text-sm font-semibold"
        style={{ color: "var(--color-text-primary)" }}
      >
        Dashboard
      </h2>

      {/* Overall progress */}
      <div
        className="p-4"
        style={{
          border: "1px solid var(--color-border)",
          backgroundColor: "var(--color-surface)",
          borderRadius: "var(--radius-lg)",
        }}
      >
        <h3
          className="text-xs font-medium mb-3"
          style={{ color: "var(--color-text-secondary)" }}
        >
          Overall Progress
        </h3>
        <ProgressBar progress={progress} showLabel height={8} />
        <p
          className="text-xs mt-2"
          style={{ color: "var(--color-text-tertiary)" }}
        >
          {stats.total} nodes total, {doneCount} done
        </p>
      </div>

      {/* Status breakdown */}
      <div
        className="p-4"
        style={{
          border: "1px solid var(--color-border)",
          backgroundColor: "var(--color-surface)",
          borderRadius: "var(--radius-lg)",
        }}
      >
        <h3
          className="text-xs font-medium mb-3"
          style={{ color: "var(--color-text-secondary)" }}
        >
          Status Breakdown
        </h3>

        {/* Stacked bar */}
        <div
          className="h-5 overflow-hidden flex"
          style={{
            backgroundColor: "var(--color-border)",
            borderRadius: "var(--radius-sm)",
          }}
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
              />
            );
          })}
        </div>

        {/* Legend */}
        <div className="flex flex-wrap gap-x-4 gap-y-2 mt-3">
          {statusEntries.map(([status, count]) => (
            <div key={status} className="flex items-center gap-1.5 text-xs">
              <StatusIcon status={status as Status} size={11} />
              <span style={{ color: "var(--color-text-secondary)" }}>
                {status.replace(/_/g, " ")}
              </span>
              <span
                className="font-medium tabular-nums"
                style={{ color: "var(--color-text-primary)" }}
              >
                {count}
              </span>
            </div>
          ))}
        </div>
      </div>

      {/* Quick stats cards */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        {statusEntries.slice(0, 4).map(([status, count]) => (
          <div
            key={status}
            className="p-3 text-center"
            style={{
              border: "1px solid var(--color-border)",
              backgroundColor: "var(--color-surface)",
              borderRadius: "var(--radius-lg)",
            }}
          >
            <div
              className="text-lg font-semibold tabular-nums"
              style={{ color: STATUS_COLORS[status] ?? "var(--color-text-primary)" }}
            >
              {count}
            </div>
            <div
              className="text-[10px] uppercase tracking-wider mt-0.5"
              style={{ color: "var(--color-text-tertiary)" }}
            >
              {status.replace(/_/g, " ")}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
