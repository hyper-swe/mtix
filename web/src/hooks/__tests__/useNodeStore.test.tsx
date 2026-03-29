import { describe, it, expect } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useNodeStore } from "../useNodeStore";
import type { Node, Status } from "../../types";

/**
 * useNodeStore tests for tree sorting (MTIX-16.1) and hide-done (MTIX-16.2).
 */

function makeNode(overrides: Partial<Node> = {}): Node {
  return {
    id: "PROJ-1",
    parent_id: "",
    project: "PROJ",
    depth: 0,
    seq: 1,
    title: "Test Node",
    description: "",
    prompt: "",
    acceptance: "",
    labels: [],
    priority: 3,
    status: "open",
    node_type: "story",
    issue_type: "task",
    creator: "",
    assignee: "",
    agent_state: "idle",
    weight: 1,
    progress: 0,
    content_hash: "",
    child_count: 0,
    created_at: null,
    updated_at: null,
    closed_at: null,
    defer_until: null,
    deleted_at: null,
    ...overrides,
  };
}

describe("useNodeStore sorting", () => {
  it("sorts root nodes by status: in_progress > open > blocked > done", () => {
    const { result } = renderHook(() => useNodeStore());

    // Add nodes in reverse order.
    act(() => {
      result.current.addNode(makeNode({ id: "N-1", seq: 1, status: "done" }));
      result.current.addNode(makeNode({ id: "N-2", seq: 2, status: "open" }));
      result.current.addNode(makeNode({ id: "N-3", seq: 3, status: "in_progress" }));
      result.current.addNode(makeNode({ id: "N-4", seq: 4, status: "blocked" }));
    });

    const items = result.current.flatTree();
    const ids = items.map((item) => item.node.id);

    expect(ids).toEqual(["N-3", "N-2", "N-4", "N-1"]);
  });

  it("preserves seq order within same status", () => {
    const { result } = renderHook(() => useNodeStore());

    act(() => {
      result.current.addNode(makeNode({ id: "N-3", seq: 3, status: "open" }));
      result.current.addNode(makeNode({ id: "N-1", seq: 1, status: "open" }));
      result.current.addNode(makeNode({ id: "N-2", seq: 2, status: "open" }));
    });

    const items = result.current.flatTree();
    const ids = items.map((item) => item.node.id);

    expect(ids).toEqual(["N-1", "N-2", "N-3"]);
  });

  it("sorts all status values correctly", () => {
    const { result } = renderHook(() => useNodeStore());
    const statuses: Status[] = ["cancelled", "done", "deferred", "blocked", "open", "in_progress"];

    act(() => {
      statuses.forEach((status, i) => {
        result.current.addNode(makeNode({ id: `N-${i}`, seq: i, status }));
      });
    });

    const items = result.current.flatTree();
    const resultStatuses = items.map((item) => item.node.status);

    expect(resultStatuses).toEqual([
      "in_progress",
      "open",
      "blocked",
      "deferred",
      "done",
      "cancelled",
    ]);
  });
});

describe("useNodeStore hideDone", () => {
  it("defaults hideDone to false", () => {
    const { result } = renderHook(() => useNodeStore());
    expect(result.current.hideDone).toBe(false);
  });

  it("shows done nodes when hideDone is false", () => {
    const { result } = renderHook(() => useNodeStore());

    act(() => {
      result.current.addNode(makeNode({ id: "N-1", seq: 1, status: "open" }));
      result.current.addNode(makeNode({ id: "N-2", seq: 2, status: "done" }));
    });

    const items = result.current.flatTree();
    expect(items).toHaveLength(2);
  });

  it("hides done and cancelled nodes when hideDone is true", () => {
    const { result } = renderHook(() => useNodeStore());

    act(() => {
      result.current.addNode(makeNode({ id: "N-1", seq: 1, status: "open" }));
      result.current.addNode(makeNode({ id: "N-2", seq: 2, status: "done" }));
      result.current.addNode(makeNode({ id: "N-3", seq: 3, status: "cancelled" }));
      result.current.addNode(makeNode({ id: "N-4", seq: 4, status: "in_progress" }));
    });

    act(() => {
      result.current.setHideDone(true);
    });

    const items = result.current.flatTree();
    const ids = items.map((item) => item.node.id);

    expect(ids).toEqual(["N-4", "N-1"]);
    expect(items).toHaveLength(2);
  });

  it("toggles hideDone back to show done nodes", () => {
    const { result } = renderHook(() => useNodeStore());

    act(() => {
      result.current.addNode(makeNode({ id: "N-1", seq: 1, status: "open" }));
      result.current.addNode(makeNode({ id: "N-2", seq: 2, status: "done" }));
    });

    act(() => { result.current.setHideDone(true); });
    expect(result.current.flatTree()).toHaveLength(1);

    act(() => { result.current.setHideDone(false); });
    expect(result.current.flatTree()).toHaveLength(2);
  });
});

describe("useNodeStore basic operations", () => {
  it("selectNode sets selectedId", () => {
    const { result } = renderHook(() => useNodeStore());

    act(() => { result.current.selectNode("N-1"); });
    expect(result.current.selectedId).toBe("N-1");

    act(() => { result.current.selectNode(null); });
    expect(result.current.selectedId).toBeNull();
  });

  it("addNode adds root nodes", () => {
    const { result } = renderHook(() => useNodeStore());

    act(() => {
      result.current.addNode(makeNode({ id: "N-1", seq: 1 }));
    });

    expect(result.current.rootIds).toContain("N-1");
    expect(result.current.nodes.get("N-1")).toBeDefined();
  });

  it("addNode with parent_id adds to childrenMap", () => {
    const { result } = renderHook(() => useNodeStore());

    act(() => {
      result.current.addNode(makeNode({ id: "P-1", seq: 1 }));
      result.current.addNode(makeNode({ id: "C-1", seq: 1, parent_id: "P-1", depth: 1 }));
    });

    expect(result.current.childrenMap.get("P-1")).toContain("C-1");
    expect(result.current.rootIds).not.toContain("C-1");
  });

  it("removeNode removes from store and rootIds", () => {
    const { result } = renderHook(() => useNodeStore());

    act(() => {
      result.current.addNode(makeNode({ id: "N-1", seq: 1 }));
    });
    expect(result.current.rootIds).toContain("N-1");

    act(() => {
      result.current.removeNode("N-1");
    });
    expect(result.current.rootIds).not.toContain("N-1");
    expect(result.current.nodes.has("N-1")).toBe(false);
  });

  it("removeNode removes from parent childrenMap", () => {
    const { result } = renderHook(() => useNodeStore());

    act(() => {
      result.current.addNode(makeNode({ id: "P-1", seq: 1 }));
      result.current.addNode(makeNode({ id: "C-1", seq: 1, parent_id: "P-1", depth: 1 }));
    });
    expect(result.current.childrenMap.get("P-1")).toContain("C-1");

    act(() => {
      result.current.removeNode("C-1");
    });
    expect(result.current.childrenMap.get("P-1")).not.toContain("C-1");
  });

  it("updateNode merges fields", () => {
    const { result } = renderHook(() => useNodeStore());

    act(() => {
      result.current.addNode(makeNode({ id: "N-1", seq: 1, title: "Old" }));
    });

    act(() => {
      result.current.updateNode("N-1", { title: "New" });
    });

    expect(result.current.nodes.get("N-1")?.title).toBe("New");
  });

  it("updateNode does nothing for non-existent node", () => {
    const { result } = renderHook(() => useNodeStore());

    act(() => {
      result.current.updateNode("NONEXISTENT", { title: "Nope" });
    });

    expect(result.current.nodes.has("NONEXISTENT")).toBe(false);
  });

  it("flatTree sets hasChildren based on child_count", () => {
    const { result } = renderHook(() => useNodeStore());

    act(() => {
      result.current.addNode(makeNode({ id: "P-1", seq: 1, child_count: 3 }));
      result.current.addNode(makeNode({ id: "L-1", seq: 2, child_count: 0 }));
    });

    const items = result.current.flatTree();
    const parent = items.find((i) => i.node.id === "P-1");
    const leaf = items.find((i) => i.node.id === "L-1");

    expect(parent?.hasChildren).toBe(true);
    expect(leaf?.hasChildren).toBe(true); // not loaded yet, assumes expandable
  });
});
