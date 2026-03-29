/**
 * BreadcrumbProgress — bottom bar progress summary.
 * Shows overall progress, open count, and active agent count.
 * Per FR-UI-10 and requirement-ui.md § 8.2.
 */

import { useMemo } from "react";
import { ProgressBar } from "./ProgressBar";

export interface BreadcrumbProgressProps {
  /** Overall progress 0-1. */
  progress: number;
  /** Number of open nodes. */
  openCount: number;
  /** Number of active agents. */
  activeAgents: number;
  /** Additional CSS class. */
  className?: string;
}

export function BreadcrumbProgress({
  progress,
  openCount,
  activeAgents,
  className = "",
}: BreadcrumbProgressProps) {
  const percent = useMemo(
    () => Math.round(Math.max(0, Math.min(1, progress)) * 100),
    [progress],
  );

  return (
    <div
      className={`flex items-center gap-3 text-xs ${className}`}
      style={{ color: "var(--color-text-secondary)" }}
      data-testid="breadcrumb-progress"
    >
      <div className="w-32">
        <ProgressBar progress={progress} height={4} />
      </div>
      <span className="tabular-nums">{percent}% overall</span>
      <span aria-hidden="true">·</span>
      <span className="tabular-nums">{openCount} open</span>
      <span aria-hidden="true">·</span>
      <span className="tabular-nums">
        {activeAgents} agent{activeAgents !== 1 ? "s" : ""} active
      </span>
    </div>
  );
}
