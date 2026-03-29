import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { DependencyGraph } from "../DependencyGraph";
import type { Node, Dependency } from "../../types";

/**
 * Dependency graph tests per MTIX-9.4.3.
 * Tests nodes/edges rendering, dependency types, click navigation,
 * zoom/pan, and layouts.
 */

function makeNode(id: string, title: string, depth: number, parentId = ""): Node {
  return {
    id,
    parent_id: parentId,
    project: "PROJ",
    depth,
    seq: 1,
    title,
    description: "",
    prompt: "",
    acceptance: "",
    labels: [],
    priority: 3 as const,
    status: "open",
    node_type: "issue",
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
  };
}

const mockNodes: Node[] = [
  makeNode("PROJ-1", "Story", 0),
  makeNode("PROJ-1.1", "Epic", 1, "PROJ-1"),
  makeNode("PROJ-1.2", "Epic 2", 1, "PROJ-1"),
];

const mockDeps: Dependency[] = [
  {
    from_id: "PROJ-1.1",
    to_id: "PROJ-1.2",
    dep_type: "blocks",
    created_at: "2026-03-08T10:00:00Z",
  },
];

describe("DependencyGraph", () => {
  it("renders nodes and edges", () => {
    render(
      <DependencyGraph
        nodes={mockNodes}
        dependencies={mockDeps}
        onNavigate={vi.fn()}
      />,
    );

    expect(screen.getByTestId("dependency-graph")).toBeInTheDocument();
    expect(screen.getByTestId("graph-svg")).toBeInTheDocument();
    expect(screen.getByTestId("graph-node-PROJ-1")).toBeInTheDocument();
    expect(screen.getByTestId("graph-node-PROJ-1.1")).toBeInTheDocument();
    expect(screen.getByTestId("graph-node-PROJ-1.2")).toBeInTheDocument();
  });

  it("renders parent-child edges", () => {
    render(
      <DependencyGraph
        nodes={mockNodes}
        dependencies={[]}
        onNavigate={vi.fn()}
      />,
    );

    const parentChildEdges = screen.getAllByTestId("edge-parent-child");
    expect(parentChildEdges.length).toBeGreaterThan(0);
  });

  it("renders dependency edges with dashed style", () => {
    render(
      <DependencyGraph
        nodes={mockNodes}
        dependencies={mockDeps}
        onNavigate={vi.fn()}
      />,
    );

    const blockEdges = screen.getAllByTestId("edge-blocks");
    expect(blockEdges).toHaveLength(1);
    expect(blockEdges[0]).toHaveAttribute("stroke-dasharray", "6,3");
  });

  it("uses correct colors for dependency types", () => {
    render(
      <DependencyGraph
        nodes={mockNodes}
        dependencies={mockDeps}
        onNavigate={vi.fn()}
      />,
    );

    const blockEdge = screen.getByTestId("edge-blocks");
    expect(blockEdge.getAttribute("stroke")).toBe(
      "var(--color-status-blocked)",
    );
  });

  it("navigates when node is clicked", () => {
    const onNavigate = vi.fn();
    render(
      <DependencyGraph
        nodes={mockNodes}
        dependencies={mockDeps}
        onNavigate={onNavigate}
      />,
    );

    fireEvent.click(screen.getByTestId("graph-node-PROJ-1.1"));
    expect(onNavigate).toHaveBeenCalledWith("PROJ-1.1");
  });

  it("has zoom controls", () => {
    render(
      <DependencyGraph
        nodes={mockNodes}
        dependencies={mockDeps}
        onNavigate={vi.fn()}
      />,
    );

    expect(screen.getByTestId("zoom-controls")).toBeInTheDocument();
    expect(screen.getByTestId("zoom-in")).toBeInTheDocument();
    expect(screen.getByTestId("zoom-out")).toBeInTheDocument();

    // Click zoom in.
    fireEvent.click(screen.getByTestId("zoom-in"));
    fireEvent.click(screen.getByTestId("zoom-out"));
  });

  it("highlights specified node", () => {
    render(
      <DependencyGraph
        nodes={mockNodes}
        dependencies={mockDeps}
        highlightedId="PROJ-1.1"
        onNavigate={vi.fn()}
      />,
    );

    const node = screen.getByTestId("graph-node-PROJ-1.1");
    const circle = node.querySelector("circle");
    expect(circle?.getAttribute("stroke")).toBe("var(--color-accent)");
  });

  it("shows node labels", () => {
    render(
      <DependencyGraph
        nodes={mockNodes}
        dependencies={mockDeps}
        onNavigate={vi.fn()}
      />,
    );

    expect(screen.getByTestId("node-label-PROJ-1")).toHaveTextContent("Story");
  });

  it("shows empty state", () => {
    render(
      <DependencyGraph
        nodes={[]}
        dependencies={[]}
        onNavigate={vi.fn()}
      />,
    );

    expect(screen.getByText("No nodes to display")).toBeInTheDocument();
  });
});
