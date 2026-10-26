/**
 * StatusBadge — inline status badge with transition popover.
 * Per FR-UI-13: hover reveals valid next states per FR-3.5.
 * Single click for forward transitions, confirmation for destructive ones.
 * Per requirement-ui.md section 8.5.
 */

import { useState, useCallback, useRef, useEffect } from "react";
import type { Status } from "../types";

/** Valid status transitions per FR-3.5 state machine. */
const TRANSITIONS: Record<Status, Status[]> = {
  open: ["in_progress", "deferred", "cancelled"],
  in_progress: ["done", "blocked", "deferred", "cancelled"],
  blocked: ["in_progress", "open", "cancelled"],
  done: ["open"],
  deferred: ["open", "cancelled"],
  cancelled: ["open"],
  invalidated: ["open"],
};

/** Destructive transitions that require confirmation. */
const DESTRUCTIVE: Set<Status> = new Set(["cancelled"]);

/** Status display labels. */
const STATUS_LABELS: Record<Status, string> = {
  open: "OPEN",
  in_progress: "IN PROGRESS",
  blocked: "BLOCKED",
  done: "DONE",
  deferred: "DEFERRED",
  cancelled: "CANCELLED",
  invalidated: "INVALIDATED",
};

/** Status background CSS vars. */
const STATUS_BG: Record<Status, string> = {
  open: "var(--color-status-open-bg)",
  in_progress: "var(--color-status-in-progress-bg)",
  blocked: "var(--color-status-blocked-bg)",
  done: "var(--color-status-done-bg)",
  deferred: "var(--color-status-deferred-bg)",
  cancelled: "var(--color-status-cancelled-bg)",
  invalidated: "var(--color-status-invalidated-bg)",
};

export interface StatusBadgeProps {
  /** Current status. */
  status: Status;
  /** Callback when status should change. */
  onStatusChange: (newStatus: Status) => void;
  /** Additional CSS class. */
  className?: string;
}

export function StatusBadge({
  status,
  onStatusChange,
  className = "",
}: StatusBadgeProps) {
  const [showTransitions, setShowTransitions] = useState(false);
  const [confirmStatus, setConfirmStatus] = useState<Status | null>(null);
  const popoverRef = useRef<HTMLDivElement>(null);

  // Close popover on outside click.
  useEffect(() => {
    if (!showTransitions && !confirmStatus) return;
    const handler = (e: MouseEvent) => {
      if (
        popoverRef.current &&
        !popoverRef.current.contains(e.target as globalThis.Node)
      ) {
        setShowTransitions(false);
        setConfirmStatus(null);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showTransitions, confirmStatus]);

  const handleTransitionClick = useCallback(
    (targetStatus: Status) => {
      if (DESTRUCTIVE.has(targetStatus)) {
        setConfirmStatus(targetStatus);
      } else {
        onStatusChange(targetStatus);
        setShowTransitions(false);
      }
    },
    [onStatusChange],
  );

  const handleConfirm = useCallback(() => {
    if (confirmStatus) {
      onStatusChange(confirmStatus);
      setConfirmStatus(null);
      setShowTransitions(false);
    }
  }, [confirmStatus, onStatusChange]);

  const validTransitions = TRANSITIONS[status] ?? [];
  const statusColor = `var(--color-status-${status.replace("_", "-")})`;

  return (
    <div className={`relative inline-block ${className}`} ref={popoverRef}>
      <button
        className="px-2.5 py-1 text-[11px] font-semibold rounded cursor-pointer flex items-center gap-1.5"
        style={{
          backgroundColor: STATUS_BG[status] ?? "var(--color-hover)",
          color: statusColor,
          borderRadius: "var(--radius-md)",
        }}
        onClick={() => setShowTransitions(!showTransitions)}
        onMouseEnter={() => setShowTransitions(true)}
        data-testid="status-badge"
        aria-label={`Status: ${STATUS_LABELS[status]}`}
      >
        <span
          className="w-1.5 h-1.5 rounded-full"
          style={{ backgroundColor: statusColor }}
        />
        {STATUS_LABELS[status]}
      </button>

      {/* Transition popover */}
      {showTransitions && validTransitions.length > 0 && !confirmStatus && (
        <div
          className="absolute left-0 top-full mt-1 z-10 py-1 min-w-[140px] animate-slide-down"
          style={{
            backgroundColor: "var(--color-surface-overlay)",
            borderRadius: "var(--radius-lg)",
            boxShadow: "var(--shadow-lg)",
            border: "1px solid var(--color-border)",
          }}
          data-testid="transition-popover"
        >
          {validTransitions.map((t) => (
            <button
              key={t}
              className="w-full flex items-center gap-2 px-3 py-1.5 text-xs text-left cursor-pointer"
              style={{
                color: "var(--color-text-secondary)",
              }}
              onMouseEnter={(e) => (e.currentTarget.style.backgroundColor = "var(--color-hover)")}
              onMouseLeave={(e) => (e.currentTarget.style.backgroundColor = "transparent")}
              onClick={() => handleTransitionClick(t)}
              data-testid={`transition-${t}`}
            >
              <span
                className="w-1.5 h-1.5 rounded-full flex-shrink-0"
                style={{ backgroundColor: `var(--color-status-${t.replace("_", "-")})` }}
              />
              {STATUS_LABELS[t]}
            </button>
          ))}
        </div>
      )}

      {/* Destructive confirmation popover */}
      {confirmStatus && (
        <div
          className="absolute left-0 top-full mt-1 z-10 p-3 min-w-[180px] animate-slide-down"
          style={{
            backgroundColor: "var(--color-surface-overlay)",
            borderRadius: "var(--radius-lg)",
            boxShadow: "var(--shadow-lg)",
            border: "1px solid var(--color-status-blocked)",
          }}
          data-testid="confirm-popover"
        >
          <p
            className="text-xs mb-2.5"
            style={{ color: "var(--color-text-primary)" }}
          >
            Confirm {STATUS_LABELS[confirmStatus]}?
          </p>
          <div className="flex gap-2">
            <button
              className="px-2.5 py-1 text-xs rounded cursor-pointer font-medium"
              style={{
                backgroundColor: "var(--color-status-blocked)",
                color: "#ffffff",
                borderRadius: "var(--radius-md)",
              }}
              onClick={handleConfirm}
              data-testid="confirm-destructive"
            >
              Confirm
            </button>
            <button
              className="px-2.5 py-1 text-xs rounded cursor-pointer"
              style={{
                color: "var(--color-text-secondary)",
                border: "1px solid var(--color-border)",
                borderRadius: "var(--radius-md)",
              }}
              onClick={() => setConfirmStatus(null)}
              data-testid="cancel-destructive"
            >
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
