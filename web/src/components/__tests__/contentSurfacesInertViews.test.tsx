import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";

vi.mock("../../api", () => ({
  listNodes: vi.fn(),
  searchNodes: vi.fn(),
}));
vi.mock("../../contexts/NavigationContext", () => ({ useNavigation: vi.fn() }));
vi.mock("../../contexts/WebSocketContext", () => ({
  useWebSocket: vi.fn().mockReturnValue({
    status: "connected",
    subscribe: vi.fn().mockReturnValue(() => {}),
    subscribeAll: vi.fn().mockReturnValue(() => {}),
  }),
}));

import * as api from "../../api";
import { useNavigation } from "../../contexts/NavigationContext";
import { KanbanView } from "../KanbanView";
import { TreeView } from "../TreeView";
import { NodeListView } from "../NodeListView";
import { StaleBoard } from "../StaleBoard";
import { CommandPalette } from "../CommandPalette";
import { DependencyGraph } from "../DependencyGraph";
import { Breadcrumb } from "../Breadcrumb";
import { Dashboard } from "../Dashboard";
import { FilterPanel } from "../FilterPanel";
import { ThemeProvider } from "../../contexts/ThemeContext";
import { expectInertText, HOSTILE, HOSTILE_SHORT, makeNode } from "./contentInertHelpers";
import type { TreeItem } from "../../hooks/useNodeStore";
import type { StaleEntry, Stats } from "../../types";

/**
 * Hardening regressions for the list, board, tree, graph, palette, stale,
 * breadcrumb, dashboard and filter surfaces (NFR-5.1): node titles, assignee
 * and agent names, and story titles render hostile content as literal text and
 * build no element or URL attribute from it.
 */

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
});

const titleNode = makeNode({ id: "PROJ-1", title: HOSTILE, assignee: "", status: "open" });
const assigneeNode = makeNode({ id: "PROJ-2", title: "Plain", assignee: HOSTILE, status: "open" });
const hostileNodes = [titleNode, assigneeNode];

describe("hostile content stays inert on list and board surfaces", () => {
  it("kanban card titles and assignees", async () => {
    vi.mocked(useNavigation).mockReturnValue({
      view: "kanban", selectedNodeId: null, selectNode: vi.fn(), navigateTo: vi.fn(), goBack: vi.fn(),
    } as unknown as ReturnType<typeof useNavigation>);
    vi.mocked(api.listNodes).mockResolvedValue({ nodes: hostileNodes, total: 2, has_more: false });
    const store = {
      nodes: new Map(), expanded: new Set<string>(), selectedId: null, rootIds: [] as string[],
      childrenMap: new Map(), loadedChildren: new Set<string>(), loadingChildren: new Set<string>(),
      loading: false, hideDone: false, setHideDone: vi.fn(), loadRoots: vi.fn(), loadChildren: vi.fn(),
      toggleExpand: vi.fn(), selectNode: vi.fn(), updateNode: vi.fn(), addNode: vi.fn(),
      removeNode: vi.fn(), flatTree: vi.fn().mockReturnValue([]),
    };
    const { container } = render(<KanbanView nodeStore={store as never} />);
    await waitFor(() => expect(screen.getByTestId("kanban-column-open")).toBeInTheDocument());
    await waitFor(() => expectInertText(container, HOSTILE, 2));
  });

  it("tree row titles", () => {
    const items: TreeItem[] = [
      { node: titleNode, depth: 0, isExpanded: false, hasChildren: false, isLoadingChildren: false },
    ];
    const { container } = render(
      <ThemeProvider>
        <TreeView items={items} selectedId={null} onSelect={vi.fn()} onToggleExpand={vi.fn()} />
      </ThemeProvider>,
    );
    expectInertText(container);
  });

  it("node list titles and assignees", async () => {
    vi.mocked(api.listNodes).mockResolvedValue({ nodes: hostileNodes, total: 2, has_more: false });
    const { container } = render(<NodeListView onSelectNode={vi.fn()} />);
    await waitFor(() => expectInertText(container, HOSTILE, 2));
  });

  it("stale board titles and agents", () => {
    const entries: StaleEntry[] = [
      { node: titleNode, reason: "in_progress_too_long", time_since_activity: 7200, assigned_agent: HOSTILE },
    ];
    const { container } = render(<StaleBoard entries={entries} onAction={vi.fn()} onNavigate={vi.fn()} />);
    expectInertText(container, HOSTILE, 2);
  });

  it("command palette search result titles", async () => {
    vi.mocked(api.searchNodes).mockResolvedValue([titleNode]);
    render(<CommandPalette isOpen={true} onClose={vi.fn()} onSelectNode={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("Search"), { target: { value: "x" } });
    await waitFor(() => expectInertText(screen.getByRole("dialog")), { timeout: 2000 });
  });

  it("dependency graph node titles", () => {
    const nodes = [makeNode({ id: "PROJ-1", title: HOSTILE_SHORT, depth: 0 })];
    const { container } = render(<DependencyGraph nodes={nodes} dependencies={[]} onNavigate={vi.fn()} />);
    expectInertText(container, HOSTILE_SHORT);
    expect(container.querySelector("b")).toBeNull();
  });

  it("breadcrumb selected node label", () => {
    vi.mocked(useNavigation).mockReturnValue({
      view: "node-detail", selectedNodeId: HOSTILE, selectNode: vi.fn(), navigateTo: vi.fn(), goBack: vi.fn(),
    } as unknown as ReturnType<typeof useNavigation>);
    const { container } = render(<Breadcrumb />);
    expectInertText(container);
  });

  it("dashboard story titles and agent names", () => {
    const stats: Stats = {
      total_nodes: 1, by_status: { open: 1 }, by_priority: {}, by_type: {}, progress: 0, scope_id: "PROJ",
    } as unknown as Stats;
    const { container } = render(
      <Dashboard
        stats={stats}
        stories={[{ id: "PROJ-1", title: HOSTILE, progress: 0.5, status: "open", childCount: 1 }]}
        agentStats={[{ agentId: HOSTILE, completed: 1, inProgress: 0 }]}
        burndown={[]}
        onNavigate={vi.fn()}
      />,
    );
    expectInertText(container, HOSTILE, 2);
  });

  it("filter panel assignee names", () => {
    const filters = {
      statuses: new Set(), priorities: new Set(), assignees: new Set(), types: new Set(),
    } as never;
    const { container } = render(
      <FilterPanel
        filters={filters}
        onToggleStatus={vi.fn()}
        onTogglePriority={vi.fn()}
        onToggleAssignee={vi.fn()}
        onToggleType={vi.fn()}
        onClearAll={vi.fn()}
        hasActiveFilters={false}
        knownAssignees={[HOSTILE]}
      />,
    );
    expectInertText(container);
  });
});
