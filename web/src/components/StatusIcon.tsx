import type { Status } from "../types";

interface StatusIconProps {
  status: Status;
  size?: number;
}

/**
 * Status icon with color per requirement-ui.md §9.
 * Uses CSS custom properties for theme-aware colors.
 */
export function StatusIcon({ status, size = 12 }: StatusIconProps) {
  const color = statusColor(status);

  // Done gets a filled circle with checkmark.
  if (status === "done") {
    return (
      <svg width={size} height={size} viewBox="0 0 12 12" aria-label="done">
        <circle cx="6" cy="6" r="5" fill={color} />
        <path
          d="M3.5 6L5.5 8L8.5 4.5"
          stroke="white"
          strokeWidth="1.5"
          strokeLinecap="round"
          strokeLinejoin="round"
          fill="none"
        />
      </svg>
    );
  }

  // In-progress gets a half-filled circle.
  if (status === "in_progress") {
    return (
      <svg width={size} height={size} viewBox="0 0 12 12" aria-label="in progress">
        <circle cx="6" cy="6" r="5" fill="none" stroke={color} strokeWidth="1.5" />
        <path d="M6 1A5 5 0 0 1 6 11" fill={color} />
      </svg>
    );
  }

  // Blocked gets an X.
  if (status === "blocked") {
    return (
      <svg width={size} height={size} viewBox="0 0 12 12" aria-label="blocked">
        <circle cx="6" cy="6" r="5" fill="none" stroke={color} strokeWidth="1.5" />
        <line x1="4" y1="4" x2="8" y2="8" stroke={color} strokeWidth="1.5" strokeLinecap="round" />
        <line x1="8" y1="4" x2="4" y2="8" stroke={color} strokeWidth="1.5" strokeLinecap="round" />
      </svg>
    );
  }

  // Invalidated gets a warning triangle.
  if (status === "invalidated") {
    return (
      <svg width={size} height={size} viewBox="0 0 12 12" aria-label="invalidated">
        <path
          d="M6 1L11 10H1L6 1Z"
          fill="none"
          stroke={color}
          strokeWidth="1.2"
          strokeLinejoin="round"
        />
        <line x1="6" y1="4.5" x2="6" y2="7" stroke={color} strokeWidth="1.2" strokeLinecap="round" />
        <circle cx="6" cy="8.5" r="0.5" fill={color} />
      </svg>
    );
  }

  // Deferred gets a pause icon.
  if (status === "deferred") {
    return (
      <svg width={size} height={size} viewBox="0 0 12 12" aria-label="deferred">
        <circle cx="6" cy="6" r="5" fill="none" stroke={color} strokeWidth="1.5" />
        <line x1="4.5" y1="4" x2="4.5" y2="8" stroke={color} strokeWidth="1.5" strokeLinecap="round" />
        <line x1="7.5" y1="4" x2="7.5" y2="8" stroke={color} strokeWidth="1.5" strokeLinecap="round" />
      </svg>
    );
  }

  // Cancelled gets a strikethrough circle.
  if (status === "cancelled") {
    return (
      <svg width={size} height={size} viewBox="0 0 12 12" aria-label="cancelled">
        <circle cx="6" cy="6" r="5" fill="none" stroke={color} strokeWidth="1.5" />
        <line x1="3" y1="6" x2="9" y2="6" stroke={color} strokeWidth="1.5" strokeLinecap="round" />
      </svg>
    );
  }

  // Open (default) gets an empty circle.
  return (
    <svg width={size} height={size} viewBox="0 0 12 12" aria-label="open">
      <circle cx="6" cy="6" r="5" fill="none" stroke={color} strokeWidth="1.5" />
    </svg>
  );
}

/** Get CSS variable name for a status color. */
function statusColor(status: Status): string {
  const map: Record<Status, string> = {
    open: "var(--color-status-open)",
    in_progress: "var(--color-status-in-progress)",
    blocked: "var(--color-status-blocked)",
    done: "var(--color-status-done)",
    deferred: "var(--color-status-deferred)",
    cancelled: "var(--color-status-cancelled)",
    invalidated: "var(--color-status-invalidated)",
  };
  return map[status] ?? "var(--color-status-open)";
}
