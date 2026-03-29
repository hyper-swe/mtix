/**
 * ProgressBar — horizontal bar showing completion percentage.
 * Color transitions from red (0-25%) to yellow (25-75%) to green (75-100%).
 * Used in node detail header, tree nodes, and breadcrumb bar.
 * Per FR-5.7 and requirement-ui.md § 8.2.
 */

import { useMemo } from "react";

/** Get color class based on percentage range. */
function getProgressColor(percent: number): string {
  if (percent >= 75) return "var(--color-status-done)";
  if (percent >= 25) return "var(--color-status-in-progress)";
  return "var(--color-status-blocked)";
}

export interface ProgressBarProps {
  /** Progress value 0-1. */
  progress: number;
  /** Height in pixels. Default 6. */
  height?: number;
  /** Show percentage label. Default false. */
  showLabel?: boolean;
  /** Additional CSS class. */
  className?: string;
}

export function ProgressBar({
  progress,
  height = 6,
  showLabel = false,
  className = "",
}: ProgressBarProps) {
  const percent = useMemo(
    () => Math.round(Math.max(0, Math.min(1, progress)) * 100),
    [progress],
  );

  const color = useMemo(() => getProgressColor(percent), [percent]);

  return (
    <div
      className={`flex items-center gap-2 ${className}`}
      role="progressbar"
      aria-valuenow={percent}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-label={`${percent}% complete`}
    >
      <div
        className="flex-1 rounded-full overflow-hidden"
        style={{
          height: `${height}px`,
          backgroundColor: "var(--color-border)",
        }}
      >
        <div
          className="h-full rounded-full"
          data-testid="progress-fill"
          style={{
            width: `${percent}%`,
            backgroundColor: color,
            transition: "width 300ms ease, background-color 300ms ease",
          }}
        />
      </div>
      {showLabel && (
        <span
          className="text-xs tabular-nums shrink-0"
          style={{ color: "var(--color-text-secondary)", minWidth: "3ch" }}
        >
          {percent}%
        </span>
      )}
    </div>
  );
}
