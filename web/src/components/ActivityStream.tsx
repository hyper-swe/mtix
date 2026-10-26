/**
 * Paginated activity supplied by the node detail view, newest first (FR-3.6a).
 * Comments include annotations; rendering a message never acknowledges its inbox event.
 */
import { useState, useMemo } from "react";
import type { ActivityEntry } from "../types";
import { ActivityEntryRow } from "./ActivityEntryRow";

export type ActivityFilter = "all" | "status" | "comment" | "agent" | "edit";

function entryCategory(entry: ActivityEntry): ActivityFilter {
  if (entry.type === "status_change") return "status";
  if (entry.type === "comment") return "comment";
  if (entry.type.startsWith("agent_")) return "agent";
  if (entry.type === "prompt_edit") return "edit";
  return "all";
}

function ActivityFilters({ filter, onChange }: {
  filter: ActivityFilter;
  onChange: (filter: ActivityFilter) => void;
}) {
  const filters: { label: string; value: ActivityFilter }[] = [
    { label: "All", value: "all" }, { label: "Status", value: "status" },
    { label: "Comments", value: "comment" }, { label: "Agent", value: "agent" },
    { label: "Edits", value: "edit" },
  ];
  return (
    <div className="flex gap-1 mb-3 pb-2 border-b" style={{ borderColor: "var(--color-border)" }}>
      {filters.map((item) => (
        <button key={item.value} className="px-2 py-1 text-xs rounded cursor-pointer"
          style={{ backgroundColor: filter === item.value ? "var(--color-accent)" : "transparent",
            color: filter === item.value ? "#FFFFFF" : "var(--color-text-secondary)" }}
          onClick={() => onChange(item.value)} data-testid={`filter-${item.value}`}>
          {item.label}
        </button>
      ))}
    </div>
  );
}

export interface ActivityStreamProps {
  entries: ActivityEntry[];
  hasMore: boolean;
  onLoadMore: () => void;
  loading?: boolean;
  className?: string;
}

export function ActivityStream({ entries, hasMore, onLoadMore, loading = false, className = "" }: ActivityStreamProps) {
  const [filter, setFilter] = useState<ActivityFilter>("all");
  const filteredEntries = useMemo(() => filter === "all" ? entries : entries.filter((entry) => entryCategory(entry) === filter), [entries, filter]);
  return (
    <div className={className} data-testid="activity-stream">
      <ActivityFilters filter={filter} onChange={setFilter} />
      {filteredEntries.length === 0 ? (
        <p className="text-xs py-4 text-center" style={{ color: "var(--color-text-secondary)" }}>No activity</p>
      ) : (
        <div className="space-y-0">{filteredEntries.map((entry) => <ActivityEntryRow key={entry.id} entry={entry} />)}</div>
      )}
      {hasMore && (
        <button className="w-full text-center py-2 text-xs cursor-pointer mt-2"
          style={{ color: "var(--color-accent)" }} onClick={onLoadMore} disabled={loading} data-testid="load-more">
          {loading ? "Loading…" : "Load more"}
        </button>
      )}
    </div>
  );
}
