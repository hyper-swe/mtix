import { describe, it, expect, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useFilters, filtersToParams, loadPresets } from "../useFilters";

/**
 * Filter tests per MTIX-9.2.4.
 * Tests status/priority/assignee/type filtering, AND/OR logic,
 * URL sync, chips, clear all, and presets.
 */

beforeEach(() => {
  window.localStorage.clear();
  // Reset URL.
  window.history.replaceState(null, "", "/");
});

describe("useFilters", () => {
  it("toggles status filter", () => {
    const { result } = renderHook(() => useFilters());

    act(() => {
      result.current.toggleStatus("open");
    });

    expect(result.current.filters.statuses.has("open")).toBe(true);

    act(() => {
      result.current.toggleStatus("open");
    });

    expect(result.current.filters.statuses.has("open")).toBe(false);
  });

  it("toggles priority filter", () => {
    const { result } = renderHook(() => useFilters());

    act(() => {
      result.current.togglePriority(1);
    });

    expect(result.current.filters.priorities.has(1)).toBe(true);
  });

  it("toggles assignee filter", () => {
    const { result } = renderHook(() => useFilters());

    act(() => {
      result.current.toggleAssignee("agent-claude");
    });

    expect(result.current.filters.assignees.has("agent-claude")).toBe(true);
  });

  it("toggles type filter", () => {
    const { result } = renderHook(() => useFilters());

    act(() => {
      result.current.toggleType("issue");
    });

    expect(result.current.filters.types.has("issue")).toBe(true);
  });

  it("multiple same-filter values are OR'd", () => {
    const { result } = renderHook(() => useFilters());

    act(() => {
      result.current.toggleStatus("open");
      result.current.toggleStatus("in_progress");
    });

    // Both statuses active — OR logic.
    const params = filtersToParams(result.current.filters);
    expect(params.get("status")).toBe("open,in_progress");
  });

  it("different filters are AND'd — multiple params present", () => {
    const { result } = renderHook(() => useFilters());

    act(() => {
      result.current.toggleStatus("open");
      result.current.togglePriority(1);
    });

    const params = filtersToParams(result.current.filters);
    expect(params.get("status")).toBe("open");
    expect(params.get("priority")).toBe("1");
  });

  it("generates active filter chips", () => {
    const { result } = renderHook(() => useFilters());

    act(() => {
      result.current.toggleStatus("open");
      result.current.togglePriority(2);
    });

    const chips = result.current.activeChips();
    expect(chips).toHaveLength(2);
    expect(chips.some((c) => c.label === "Status: open")).toBe(true);
    expect(chips.some((c) => c.label === "Priority: 2")).toBe(true);
  });

  it("clears all filters", () => {
    const { result } = renderHook(() => useFilters());

    act(() => {
      result.current.toggleStatus("open");
      result.current.togglePriority(1);
      result.current.toggleType("issue");
    });

    expect(result.current.hasActiveFilters).toBe(true);

    act(() => {
      result.current.clearAll();
    });

    expect(result.current.hasActiveFilters).toBe(false);
    expect(result.current.filters.statuses.size).toBe(0);
    expect(result.current.filters.priorities.size).toBe(0);
  });

  it("syncs filters to URL query string", () => {
    const { result } = renderHook(() => useFilters());

    act(() => {
      result.current.toggleStatus("done");
    });

    expect(window.location.search).toContain("status=done");
  });

  it("saves and loads filter presets", () => {
    const { result } = renderHook(() => useFilters());

    act(() => {
      result.current.toggleStatus("open");
      result.current.togglePriority(1);
    });

    act(() => {
      result.current.savePreset("My Preset");
    });

    const presets = loadPresets();
    expect(presets).toHaveLength(1);
    expect(presets[0]?.name).toBe("My Preset");
    expect(presets[0]?.filters.statuses).toContain("open");

    // Clear and reload.
    act(() => {
      result.current.clearAll();
    });
    expect(result.current.hasActiveFilters).toBe(false);

    act(() => {
      result.current.loadPreset(presets[0]!);
    });
    expect(result.current.filters.statuses.has("open")).toBe(true);
    expect(result.current.filters.priorities.has(1)).toBe(true);
  });
});
