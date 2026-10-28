/** Shared hostile IDs/text stay inert across FR-UI tree, board and detail surfaces. */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { hostileFixtures } from "../../test/hostileFixtures";
import { makeNode, expectNoLiveContent } from "./contentInertHelpers";
import { TreeView } from "../TreeView";
import { NodeDetail } from "../NodeDetail";
import { PromptEditor } from "../PromptEditor";
import { Breadcrumb } from "../Breadcrumb";
import { TopBar } from "../TopBar";
import { KanbanView } from "../KanbanView";
import { ThemeProvider } from "../../contexts/ThemeContext";
import { useNavigation } from "../../contexts/NavigationContext";
import { useProject } from "../../contexts/ProjectContext";
import * as api from "../../api";

vi.mock("../../api", () => ({ listNodes: vi.fn() }));
vi.mock("../../contexts/NavigationContext", () => ({ useNavigation: vi.fn() }));
vi.mock("../../contexts/ProjectContext", () => ({
  ALL_PROJECTS: "all", useProject: vi.fn(), useProjectOptional: () => null,
}));
vi.mock("../../contexts/WebSocketContext", () => ({ useWebSocket: () => ({
  status: "connected", subscribe: () => () => {}, subscribeAll: () => () => {},
}) }));

const detailProps = {
  children: [], contextChain: [], activityEntries: [], activityHasMore: false,
  dependencies: [], onLoadMoreActivity: vi.fn(), onUpdateTitle: vi.fn(),
  onStatusChange: vi.fn(), onSavePrompt: vi.fn(), onSaveAndRerun: vi.fn(),
  onAddAnnotation: vi.fn(), onResolveAnnotation: vi.fn(), onNavigate: vi.fn(),
  onCreateChild: vi.fn(),
};
beforeEach(() => { vi.clearAllMocks(); window.localStorage.clear(); });

describe("HostileNodes", () => {
  it.each([...hostileFixtures.ids, ...hostileFixtures.text])("renders $Name safely", async ({ Value }) => {
    const node = makeNode({ id: Value, title: Value, description: Value, prompt: Value, status: "open" });
    vi.mocked(useNavigation).mockReturnValue({ view: "node-detail", selectedNodeId: Value } as ReturnType<typeof useNavigation>);
    vi.mocked(useProject).mockReturnValue({
      projects: [{ prefix: Value, count: 1, isPrimary: true }], primary: Value,
      activeScope: Value, loading: false, isMultiProject: false,
    } as ReturnType<typeof useProject>);
    vi.mocked(api.listNodes).mockResolvedValue({ nodes: [node], total: 1, has_more: false });
    const { container } = render(<ThemeProvider>
      <TreeView items={[{ node, depth: 0, isExpanded: false, hasChildren: false, isLoadingChildren: false }]}
        selectedId={null} onSelect={vi.fn()} onToggleExpand={vi.fn()} />
      <KanbanView nodeStore={{ updateNode: vi.fn() } as never} />
      <NodeDetail {...detailProps} node={node} />
      <PromptEditor prompt={Value} annotations={[{
        id: Value, author: Value, text: Value, created_at: "2026-10-28T00:00:00Z", resolved: false,
      }]} isOpen onSave={vi.fn()} onSaveAndRerun={vi.fn()} onCancel={vi.fn()}
        onAddAnnotation={vi.fn()} onResolveAnnotation={vi.fn()} />
      <Breadcrumb /><TopBar onToggleSidebar={vi.fn()} sidebarCollapsed={false} />
    </ThemeProvider>);
    await waitFor(() => expect(container.querySelector('[data-testid="kanban-column-open"]')).not.toBeNull());
    // Exact text (rather than normalized textContent matchers) covers controls and >64 KiB.
    for (const selector of ['[data-testid="node-title"]', '[data-testid="description-content"]',
      '[data-testid="prompt-display"]', '[data-testid="annotation-item"]', "footer", '[data-testid="project-selector-single"]']) {
      expect(container.querySelector(selector)?.textContent).toContain(Value);
    }
    expect(container.querySelector('[role="tree"]')?.textContent).toContain(Value);
    expect(container.querySelector('[data-testid="kanban-column-open"]')?.textContent).toContain(Value);
    expect(container.querySelectorAll('[data-node-id]')).not.toHaveLength(0);
    for (const el of container.querySelectorAll('[data-node-id]')) expect(el.getAttribute("data-node-id")).toBe(Value);
    expectNoLiveContent(container);
  });
});
