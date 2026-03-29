/**
 * ActivityStream — chronological activity list for a node.
 * Shows status changes, prompt edits, comments, child creation, agent actions.
 * Paginates with "Load more" per FR-3.6a.
 * Real-time updates via WebSocket node.updated events.
 * Per MTIX-9.3.4.
 */

import { useState, useCallback, useMemo } from "react";
import type { ActivityEntry } from "../types";

/** Activity type for filtering. */
export type ActivityFilter = "all" | "status" | "comment" | "agent" | "edit";

/** Format a timestamp as relative time. */
function relativeTime(isoString: string): string {
  const now = Date.now();
  const then = new Date(isoString).getTime();
  const diffSec = Math.floor((now - then) / 1000);

  if (diffSec < 60) return "just now";
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)} min ago`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
  if (diffSec < 604800) return `${Math.floor(diffSec / 86400)}d ago`;
  return new Date(isoString).toLocaleDateString();
}

/** Get icon for activity type. */
function activityIcon(type: string): string {
  switch (type) {
    case "status_change": return "●";
    case "comment": return "💬";
    case "prompt_edit": return "✏";
    case "child_created": return "+";
    case "agent_claim": return "▶";
    case "agent_done": return "✓";
    default: return "·";
  }
}

/** Determine if an entry is from an agent. */
function isAgentEntry(entry: ActivityEntry): boolean {
  return entry.author.startsWith("agent-");
}

/** Determine the filter category for an entry. */
function entryCategory(entry: ActivityEntry): ActivityFilter {
  if (entry.type === "status_change") return "status";
  if (entry.type === "comment") return "comment";
  if (entry.type.startsWith("agent_")) return "agent";
  if (entry.type === "prompt_edit") return "edit";
  return "all";
}

export interface ActivityStreamProps {
  /** Activity entries (most recent first). */
  entries: ActivityEntry[];
  /** Whether more entries are available. */
  hasMore: boolean;
  /** Load more entries callback. */
  onLoadMore: () => void;
  /** Whether loading is in progress. */
  loading?: boolean;
  /** Additional CSS class. */
  className?: string;
}

export function ActivityStream({
  entries,
  hasMore,
  onLoadMore,
  loading = false,
  className = "",
}: ActivityStreamProps) {
  const [filter, setFilter] = useState<ActivityFilter>("all");

  const filteredEntries = useMemo(() => {
    if (filter === "all") return entries;
    return entries.filter((e) => entryCategory(e) === filter);
  }, [entries, filter]);

  const handleFilterChange = useCallback(
    (newFilter: ActivityFilter) => setFilter(newFilter),
    [],
  );

  const filters: { label: string; value: ActivityFilter }[] = [
    { label: "All", value: "all" },
    { label: "Status", value: "status" },
    { label: "Comments", value: "comment" },
    { label: "Agent", value: "agent" },
    { label: "Edits", value: "edit" },
  ];

  return (
    <div
      className={className}
      data-testid="activity-stream"
    >
      {/* Filter tabs */}
      <div
        className="flex gap-1 mb-3 pb-2 border-b"
        style={{ borderColor: "var(--color-border)" }}
      >
        {filters.map((f) => (
          <button
            key={f.value}
            className="px-2 py-1 text-xs rounded cursor-pointer"
            style={{
              backgroundColor:
                filter === f.value
                  ? "var(--color-accent)"
                  : "transparent",
              color:
                filter === f.value
                  ? "#FFFFFF"
                  : "var(--color-text-secondary)",
            }}
            onClick={() => handleFilterChange(f.value)}
            data-testid={`filter-${f.value}`}
          >
            {f.label}
          </button>
        ))}
      </div>

      {/* Entries */}
      {filteredEntries.length === 0 ? (
        <p
          className="text-xs py-4 text-center"
          style={{ color: "var(--color-text-secondary)" }}
        >
          No activity
        </p>
      ) : (
        <div className="space-y-0">
          {filteredEntries.map((entry) => (
            <div
              key={entry.id}
              className="flex items-start gap-2 py-2 border-b last:border-b-0"
              style={{ borderColor: "var(--color-border)" }}
              data-testid="activity-entry"
            >
              {/* Icon */}
              <span className="shrink-0 w-5 text-center text-xs mt-0.5">
                {activityIcon(entry.type)}
              </span>

              {/* Content */}
              <div className="flex-1 min-w-0">
                <div className="flex items-baseline gap-2">
                  <span
                    className="text-xs font-medium"
                    style={{
                      color: isAgentEntry(entry)
                        ? "var(--color-accent)"
                        : "var(--color-text-primary)",
                    }}
                    data-testid="activity-author"
                  >
                    {entry.author}
                  </span>
                  <span
                    className="text-xs"
                    style={{ color: "var(--color-text-secondary)" }}
                    title={new Date(entry.created_at).toLocaleString()}
                    data-testid="activity-timestamp"
                  >
                    {relativeTime(entry.created_at)}
                  </span>
                </div>
                <p
                  className="text-xs mt-0.5"
                  style={{ color: "var(--color-text-secondary)" }}
                  data-testid="activity-text"
                >
                  {entry.text}
                </p>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Load more */}
      {hasMore && (
        <button
          className="w-full text-center py-2 text-xs cursor-pointer mt-2"
          style={{ color: "var(--color-accent)" }}
          onClick={onLoadMore}
          disabled={loading}
          data-testid="load-more"
        >
          {loading ? "Loading…" : "Load more"}
        </button>
      )}
    </div>
  );
}
