/**
 * ProgressRing — circular progress indicator for compact spaces.
 * Per FR-5.7 and requirement-ui.md § 8.2.
 */

import { useMemo } from "react";

export interface ProgressRingProps {
  /** Progress value 0-1. */
  progress: number;
  /** Diameter in pixels. Default 20. */
  size?: number;
  /** Stroke width in pixels. Default 2.5. */
  strokeWidth?: number;
  /** Additional CSS class. */
  className?: string;
}

/** Get color based on percentage range. */
function getRingColor(percent: number): string {
  if (percent >= 75) return "var(--color-status-done)";
  if (percent >= 25) return "var(--color-status-in-progress)";
  if (percent > 0) return "var(--color-status-blocked)";
  return "var(--color-border)";
}

export function ProgressRing({
  progress,
  size = 20,
  strokeWidth = 2.5,
  className = "",
}: ProgressRingProps) {
  const percent = useMemo(
    () => Math.max(0, Math.min(1, progress)) * 100,
    [progress],
  );

  const radius = (size - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  const offset = circumference - (percent / 100) * circumference;
  const color = useMemo(() => getRingColor(percent), [percent]);

  return (
    <svg
      width={size}
      height={size}
      viewBox={`0 0 ${size} ${size}`}
      className={className}
      role="progressbar"
      aria-valuenow={Math.round(percent)}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-label={`${Math.round(percent)}% complete`}
    >
      {/* Background circle */}
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill="none"
        stroke="var(--color-border)"
        strokeWidth={strokeWidth}
      />
      {/* Progress arc */}
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill="none"
        stroke={color}
        strokeWidth={strokeWidth}
        strokeDasharray={circumference}
        strokeDashoffset={offset}
        strokeLinecap="round"
        transform={`rotate(-90 ${size / 2} ${size / 2})`}
        style={{ transition: "stroke-dashoffset 300ms ease, stroke 300ms ease" }}
      />
    </svg>
  );
}
