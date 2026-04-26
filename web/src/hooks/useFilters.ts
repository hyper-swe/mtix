/**
 * Filter state management per requirement-ui.md §2 View A.
 * Stores filter selections and syncs with URL query string.
 */

import { useCallback, useEffect, useState } from "react";
import type { Status, NodeType } from "../types";

export interface FilterState {
  statuses: Set<Status>;
  priorities: Set<number>;
  assignees: Set<string>;
  types: Set<NodeType>;
}

/** Storage key for saved filter presets. */
const PRESETS_KEY = "mtix-filter-presets";

/** All available statuses. */
export const ALL_STATUSES: Status[] = [
  "open",
  "in_progress",
  "blocked",
  "done",
  "deferred",
  "cancelled",
  "invalidated",
];

/**
 * All available node types in canonical hierarchical order
 * (top-down: epic → story → issue → micro). Matches Go's NodeTypeForDepth
 * post-MTIX-7 (v0.1.1-beta).
 */
export const ALL_NODE_TYPES: NodeType[] = [
  "epic",
  "story",
  "issue",
  "micro",
];

/** Build query params from filter state. */
export function filtersToParams(filters: FilterState): URLSearchParams {
  const params = new URLSearchParams();
  if (filters.statuses.size > 0) {
    params.set("status", Array.from(filters.statuses).join(","));
  }
  if (filters.priorities.size > 0) {
    params.set("priority", Array.from(filters.priorities).join(","));
  }
  if (filters.assignees.size > 0) {
    params.set("assignee", Array.from(filters.assignees).join(","));
  }
  if (filters.types.size > 0) {
    params.set("type", Array.from(filters.types).join(","));
  }
  return params;
}

/** Parse filter state from URL query string. */
function parseFiltersFromURL(): FilterState {
  const params = new URLSearchParams(window.location.search);
  return {
    statuses: new Set(
      (params.get("status")?.split(",").filter(Boolean) ?? []) as Status[],
    ),
    priorities: new Set(
      params
        .get("priority")
        ?.split(",")
        .filter(Boolean)
        .map(Number)
        .filter((n) => !isNaN(n)) ?? [],
    ),
    assignees: new Set(
      params.get("assignee")?.split(",").filter(Boolean) ?? [],
    ),
    types: new Set(
      (params.get("type")?.split(",").filter(Boolean) ?? []) as NodeType[],
    ),
  };
}

/** Named filter preset. */
export interface FilterPreset {
  name: string;
  filters: {
    statuses: string[];
    priorities: number[];
    assignees: string[];
    types: string[];
  };
}

/**
 * Hook to manage filter state with URL sync and presets.
 */
export function useFilters() {
  const [filters, setFilters] = useState<FilterState>(parseFiltersFromURL);

  // Sync filter state to URL query string.
  useEffect(() => {
    const params = filtersToParams(filters);
    const qs = params.toString();
    const newURL = qs
      ? `${window.location.pathname}?${qs}`
      : window.location.pathname;
    window.history.replaceState(null, "", newURL);
  }, [filters]);

  const toggleStatus = useCallback((status: Status) => {
    setFilters((prev) => {
      const next = new Set(prev.statuses);
      if (next.has(status)) {
        next.delete(status);
      } else {
        next.add(status);
      }
      return { ...prev, statuses: next };
    });
  }, []);

  const togglePriority = useCallback((priority: number) => {
    setFilters((prev) => {
      const next = new Set(prev.priorities);
      if (next.has(priority)) {
        next.delete(priority);
      } else {
        next.add(priority);
      }
      return { ...prev, priorities: next };
    });
  }, []);

  const toggleAssignee = useCallback((assignee: string) => {
    setFilters((prev) => {
      const next = new Set(prev.assignees);
      if (next.has(assignee)) {
        next.delete(assignee);
      } else {
        next.add(assignee);
      }
      return { ...prev, assignees: next };
    });
  }, []);

  const toggleType = useCallback((type: NodeType) => {
    setFilters((prev) => {
      const next = new Set(prev.types);
      if (next.has(type)) {
        next.delete(type);
      } else {
        next.add(type);
      }
      return { ...prev, types: next };
    });
  }, []);

  const clearAll = useCallback(() => {
    setFilters({
      statuses: new Set(),
      priorities: new Set(),
      assignees: new Set(),
      types: new Set(),
    });
  }, []);

  /** Check if any filters are active. */
  const hasActiveFilters =
    filters.statuses.size > 0 ||
    filters.priorities.size > 0 ||
    filters.assignees.size > 0 ||
    filters.types.size > 0;

  /** Get active filter chips for display. */
  const activeChips = (): Array<{ label: string; onRemove: () => void }> => {
    const chips: Array<{ label: string; onRemove: () => void }> = [];
    for (const s of filters.statuses) {
      chips.push({ label: `Status: ${s}`, onRemove: () => toggleStatus(s) });
    }
    for (const p of filters.priorities) {
      chips.push({ label: `Priority: ${p}`, onRemove: () => togglePriority(p) });
    }
    for (const a of filters.assignees) {
      chips.push({ label: `Assignee: ${a}`, onRemove: () => toggleAssignee(a) });
    }
    for (const t of filters.types) {
      chips.push({ label: `Type: ${t}`, onRemove: () => toggleType(t) });
    }
    return chips;
  };

  /** Save current filters as a preset. */
  const savePreset = useCallback(
    (name: string) => {
      const presets = loadPresets();
      const preset: FilterPreset = {
        name,
        filters: {
          statuses: Array.from(filters.statuses),
          priorities: Array.from(filters.priorities),
          assignees: Array.from(filters.assignees),
          types: Array.from(filters.types),
        },
      };
      const updated = [...presets.filter((p) => p.name !== name), preset];
      window.localStorage.setItem(PRESETS_KEY, JSON.stringify(updated));
    },
    [filters],
  );

  /** Load a saved preset. */
  const loadPreset = useCallback((preset: FilterPreset) => {
    setFilters({
      statuses: new Set(preset.filters.statuses as Status[]),
      priorities: new Set(preset.filters.priorities),
      assignees: new Set(preset.filters.assignees),
      types: new Set(preset.filters.types as NodeType[]),
    });
  }, []);

  return {
    filters,
    toggleStatus,
    togglePriority,
    toggleAssignee,
    toggleType,
    clearAll,
    hasActiveFilters,
    activeChips,
    savePreset,
    loadPreset,
  };
}

/** Load saved filter presets from localStorage. */
export function loadPresets(): FilterPreset[] {
  try {
    const stored = window.localStorage.getItem(PRESETS_KEY);
    return stored ? (JSON.parse(stored) as FilterPreset[]) : [];
  } catch {
    return [];
  }
}
