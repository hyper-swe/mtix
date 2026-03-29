import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { TreeView } from "../TreeView";
import { ThemeProvider } from "../../contexts/ThemeContext";
import type { TreeItem } from "../../hooks/useNodeStore";
import type { Node } from "../../types";

/** Wrap component with required providers. */
function renderWithProviders(ui: React.ReactElement) {
  return render(<ThemeProvider>{ui}</ThemeProvider>);
}

/**
 * Tree view tests per MTIX-9.2.1.
 * Tests hierarchy rendering, expand/collapse, status icons,
 * selection, virtualization, and drag-and-drop.
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

function makeItem(opts: {
  node?: Partial<Node>;
  depth?: number;
  isExpanded?: boolean;
  hasChildren?: boolean;
} = {}): TreeItem {
  return {
    node: makeNode(opts.node),
    depth: opts.depth ?? 0,
    isExpanded: opts.isExpanded ?? false,
    hasChildren: opts.hasChildren ?? false,
    isLoadingChildren: false,
  };
}

beforeEach(() => {
  window.localStorage.clear();
});

describe("TreeView", () => {
  it("renders hierarchy with node titles", () => {
    const items: TreeItem[] = [
      makeItem({ node: { id: "S-1", title: "Story A" }, hasChildren: true }),
      makeItem({ node: { id: "E-1.1", title: "Epic B", depth: 1 }, depth: 1 }),
    ];

    renderWithProviders(
      <TreeView
        items={items}
        selectedId={null}
        onSelect={vi.fn()}
        onToggleExpand={vi.fn()}
      />,
    );

    expect(screen.getByText("Story A")).toBeInTheDocument();
    expect(screen.getByText("Epic B")).toBeInTheDocument();
  });

  it("shows expand/collapse button for nodes with children", () => {
    const items: TreeItem[] = [
      makeItem({
        node: { id: "S-1", title: "Parent", child_count: 3 },
        hasChildren: true,
      }),
      makeItem({ node: { id: "S-2", title: "Leaf", child_count: 0 } }),
    ];

    renderWithProviders(
      <TreeView
        items={items}
        selectedId={null}
        onSelect={vi.fn()}
        onToggleExpand={vi.fn()}
      />,
    );

    // Parent should have expand button.
    expect(screen.getByLabelText("Expand")).toBeInTheDocument();
  });

  it("calls onToggleExpand when expand button is clicked", () => {
    const onToggle = vi.fn();
    const items: TreeItem[] = [
      makeItem({
        node: { id: "S-1", title: "Parent", child_count: 3 },
        hasChildren: true,
      }),
    ];

    renderWithProviders(
      <TreeView
        items={items}
        selectedId={null}
        onSelect={vi.fn()}
        onToggleExpand={onToggle}
      />,
    );

    fireEvent.click(screen.getByLabelText("Expand"));
    expect(onToggle).toHaveBeenCalledWith("S-1");
  });

  it("renders correct status icons", () => {
    const items: TreeItem[] = [
      makeItem({ node: { id: "N-1", title: "Done", status: "done" } }),
      makeItem({ node: { id: "N-2", title: "Open", status: "open" } }),
      makeItem({
        node: { id: "N-3", title: "Blocked", status: "blocked" },
      }),
    ];

    renderWithProviders(
      <TreeView
        items={items}
        selectedId={null}
        onSelect={vi.fn()}
        onToggleExpand={vi.fn()}
      />,
    );

    // Status icons have aria-label.
    expect(screen.getByLabelText("done")).toBeInTheDocument();
    expect(screen.getByLabelText("open")).toBeInTheDocument();
    expect(screen.getByLabelText("blocked")).toBeInTheDocument();
  });

  it("highlights selected node", () => {
    const items: TreeItem[] = [
      makeItem({ node: { id: "S-1", title: "Selected" } }),
      makeItem({ node: { id: "S-2", title: "Not Selected" } }),
    ];

    renderWithProviders(
      <TreeView
        items={items}
        selectedId="S-1"
        onSelect={vi.fn()}
        onToggleExpand={vi.fn()}
      />,
    );

    const selectedItem = screen.getByRole("treeitem", {
      selected: true,
    });
    expect(selectedItem).toBeInTheDocument();
  });

  it("calls onSelect when a row is clicked", () => {
    const onSelect = vi.fn();
    const items: TreeItem[] = [
      makeItem({ node: { id: "S-1", title: "Click Me" } }),
    ];

    renderWithProviders(
      <TreeView
        items={items}
        selectedId={null}
        onSelect={onSelect}
        onToggleExpand={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByText("Click Me"));
    expect(onSelect).toHaveBeenCalledWith("S-1");
  });

  it("shows progress percentage for parent nodes", () => {
    const items: TreeItem[] = [
      makeItem({
        node: { id: "S-1", title: "Parent", child_count: 5, progress: 0.66 },
      }),
    ];

    renderWithProviders(
      <TreeView
        items={items}
        selectedId={null}
        onSelect={vi.fn()}
        onToggleExpand={vi.fn()}
      />,
    );

    expect(screen.getByText("66%")).toBeInTheDocument();
  });

  it("supports drag-and-drop reparenting", () => {
    const onReparent = vi.fn();
    const items: TreeItem[] = [
      makeItem({ node: { id: "S-1", title: "Target" }, hasChildren: true }),
      makeItem({ node: { id: "S-2", title: "Draggable" } }),
    ];

    renderWithProviders(
      <TreeView
        items={items}
        selectedId={null}
        onSelect={vi.fn()}
        onToggleExpand={vi.fn()}
        onDragReparent={onReparent}
      />,
    );

    const target = screen.getByText("Target").closest("[role='treeitem']")!;
    const source = screen.getByText("Draggable").closest("[role='treeitem']")!;

    // Simulate drag-and-drop.
    const dataTransfer = {
      setData: vi.fn(),
      getData: vi.fn().mockReturnValue("S-2"),
      effectAllowed: "move",
      dropEffect: "none",
    };

    fireEvent.dragStart(source, { dataTransfer });
    fireEvent.dragOver(target, { dataTransfer });
    fireEvent.drop(target, { dataTransfer });

    expect(onReparent).toHaveBeenCalledWith("S-2", "S-1");
  });

  it("renders tree role and treeitem roles for accessibility", () => {
    const items: TreeItem[] = [
      makeItem({ node: { id: "S-1", title: "Item" } }),
    ];

    renderWithProviders(
      <TreeView
        items={items}
        selectedId={null}
        onSelect={vi.fn()}
        onToggleExpand={vi.fn()}
      />,
    );

    expect(screen.getByRole("tree")).toBeInTheDocument();
    expect(screen.getByRole("treeitem")).toBeInTheDocument();
  });
});
