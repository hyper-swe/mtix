import { useState } from "react";
import type { Status, NodeType } from "../types";
import { ALL_STATUSES, ALL_NODE_TYPES } from "../hooks/useFilters";
import type { FilterState } from "../hooks/useFilters";
import { StatusIcon } from "./StatusIcon";

interface FilterPanelProps {
  filters: FilterState;
  onToggleStatus: (status: Status) => void;
  onTogglePriority: (priority: number) => void;
  onToggleAssignee: (assignee: string) => void;
  onToggleType: (type: NodeType) => void;
  onClearAll: () => void;
  hasActiveFilters: boolean;
  knownAssignees?: string[];
}

/** Priority labels for display. */
const PRIORITY_LABELS: Record<number, string> = {
  1: "Critical",
  2: "High",
  3: "Medium",
  4: "Low",
  5: "Background",
};

/**
 * Filter panel for the sidebar per requirement-ui.md §2.
 * Collapsible filter groups: Status, Priority, Assignee, Type.
 */
export function FilterPanel({
  filters,
  onToggleStatus,
  onTogglePriority,
  onToggleAssignee,
  onToggleType,
  onClearAll,
  hasActiveFilters,
  knownAssignees = [],
}: FilterPanelProps) {
  return (
    <div className="px-3 py-2">
      <div className="flex items-center justify-between mb-1">
        <span
          className="text-xs font-semibold uppercase tracking-wider"
          style={{ color: "var(--color-text-secondary)" }}
        >
          Filters
        </span>
        {hasActiveFilters && (
          <button
            onClick={onClearAll}
            className="text-xs hover:underline"
            style={{ color: "var(--color-accent)" }}
          >
            Clear all
          </button>
        )}
      </div>

      <FilterGroup title="Status">
        {ALL_STATUSES.map((status) => (
          <FilterCheckbox
            key={status}
            checked={filters.statuses.has(status)}
            onChange={() => onToggleStatus(status)}
            label={
              <span className="flex items-center gap-1.5">
                <StatusIcon status={status} size={10} />
                <span>{formatStatus(status)}</span>
              </span>
            }
          />
        ))}
      </FilterGroup>

      <FilterGroup title="Priority">
        {[1, 2, 3, 4, 5].map((p) => (
          <FilterCheckbox
            key={p}
            checked={filters.priorities.has(p)}
            onChange={() => onTogglePriority(p)}
            label={PRIORITY_LABELS[p] ?? `P${p}`}
          />
        ))}
      </FilterGroup>

      <FilterGroup title="Assignee">
        {knownAssignees.length > 0 ? (
          knownAssignees.map((agent) => (
            <FilterCheckbox
              key={agent}
              checked={filters.assignees.has(agent)}
              onChange={() => onToggleAssignee(agent)}
              label={agent}
            />
          ))
        ) : (
          <span
            className="text-xs"
            style={{ color: "var(--color-text-secondary)" }}
          >
            No agents found
          </span>
        )}
      </FilterGroup>

      <FilterGroup title="Type">
        {ALL_NODE_TYPES.map((type) => (
          <FilterCheckbox
            key={type}
            checked={filters.types.has(type)}
            onChange={() => onToggleType(type)}
            label={type.charAt(0).toUpperCase() + type.slice(1)}
          />
        ))}
      </FilterGroup>
    </div>
  );
}

function FilterGroup({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  const [isOpen, setIsOpen] = useState(true);

  return (
    <div className="mb-2">
      <button
        className="flex items-center w-full text-left text-xs font-medium py-1"
        style={{ color: "var(--color-text-secondary)" }}
        onClick={() => setIsOpen(!isOpen)}
      >
        <span
          className="mr-1 transition-transform"
          style={{ transform: isOpen ? "rotate(90deg)" : "rotate(0deg)" }}
        >
          {"\u25B8"}
        </span>
        {title}
      </button>
      {isOpen && <div className="pl-3 space-y-0.5">{children}</div>}
    </div>
  );
}

function FilterCheckbox({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: () => void;
  label: React.ReactNode;
}) {
  return (
    <label className="flex items-center gap-1.5 text-xs cursor-pointer py-0.5">
      <input
        type="checkbox"
        checked={checked}
        onChange={onChange}
        className="w-3 h-3 rounded"
        style={{ accentColor: "var(--color-accent)" }}
      />
      <span style={{ color: "var(--color-text-primary)" }}>{label}</span>
    </label>
  );
}

function formatStatus(status: Status): string {
  return status.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
}
