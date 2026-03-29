import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { NodeDetail } from "../NodeDetail";
import type { Node, ContextEntry, Dependency } from "../../types";

/**
 * Node detail view tests per MTIX-9.3.1.
 * Tests header, inline edit, status transitions, progress bar,
 * prompt section, children list, tabbed sections, and multi-select.
 */

function makeNode(overrides: Partial<Node> = {}): Node {
  return {
    id: "PROJ-1.1.2",
    parent_id: "PROJ-1.1",
    project: "PROJ",
    depth: 2,
    seq: 2,
    title: "Fix timeout bug",
    description: "Users on slow networks see blank screen",
    prompt: "Investigate timeout in src/auth/login.go",
    acceptance: "Timeout is configurable",
    labels: [],
    priority: 2 as const,
    status: "in_progress",
    node_type: "issue",
    issue_type: "bug",
    creator: "vimal",
    assignee: "agent-claude",
    agent_state: "working",
    weight: 1,
    progress: 0.66,
    content_hash: "",
    child_count: 3,
    created_at: "2026-03-08T10:00:00Z",
    updated_at: "2026-03-08T12:00:00Z",
    closed_at: null,
    defer_until: null,
    deleted_at: null,
    annotations: [],
    ...overrides,
  };
}

function makeChild(id: string, seq: number, title: string): Node {
  return makeNode({
    id,
    seq,
    title,
    parent_id: "PROJ-1.1.2",
    status: "open",
    depth: 3,
    child_count: 0,
  });
}

const mockContext: ContextEntry[] = [
  { id: "PROJ-1", title: "User Auth", status: "in_progress", prompt: "Story prompt", acceptance: "", depth: 0 },
  { id: "PROJ-1.1", title: "Login", status: "in_progress", prompt: "Epic prompt", acceptance: "", depth: 1 },
  { id: "PROJ-1.1.2", title: "Fix timeout bug", status: "in_progress", prompt: "Issue prompt", acceptance: "", depth: 2 },
];

const mockChildren = [
  makeChild("PROJ-1.1.2.1", 1, "Make timeout configurable"),
  makeChild("PROJ-1.1.2.2", 2, "Add retry logic"),
  makeChild("PROJ-1.1.2.3", 3, "Add loading spinner"),
];

const defaultProps = {
  node: makeNode(),
  children: mockChildren,
  contextChain: mockContext,
  activityEntries: [],
  activityHasMore: false,
  onLoadMoreActivity: vi.fn(),
  dependencies: [] as Dependency[],
  onUpdateTitle: vi.fn(),
  onStatusChange: vi.fn(),
  onSavePrompt: vi.fn(),
  onSaveAndRerun: vi.fn(),
  onAddAnnotation: vi.fn(),
  onResolveAnnotation: vi.fn(),
  onNavigate: vi.fn(),
  onCreateChild: vi.fn(),
  onBulkAction: vi.fn(),
};

describe("NodeDetail", () => {
  it("renders node header with ID and title", () => {
    render(<NodeDetail {...defaultProps} />);

    expect(screen.getByTestId("node-id")).toHaveTextContent("PROJ-1.1.2");
    expect(screen.getByTestId("node-title")).toHaveTextContent(
      "Fix timeout bug",
    );
  });

  it("supports inline title editing", () => {
    const onUpdateTitle = vi.fn();
    render(
      <NodeDetail {...defaultProps} onUpdateTitle={onUpdateTitle} />,
    );

    // Click title to start editing.
    fireEvent.click(screen.getByTestId("node-title"));
    expect(screen.getByTestId("title-input")).toBeInTheDocument();

    // Change title and press Enter.
    fireEvent.change(screen.getByTestId("title-input"), {
      target: { value: "Updated title" },
    });
    fireEvent.keyDown(screen.getByTestId("title-input"), {
      key: "Enter",
    });

    expect(onUpdateTitle).toHaveBeenCalledWith("Updated title");
  });

  it("renders status badge with transitions", () => {
    render(<NodeDetail {...defaultProps} />);

    expect(screen.getByTestId("status-badge")).toHaveTextContent(
      "IN PROGRESS",
    );
  });

  it("shows priority badge", () => {
    render(<NodeDetail {...defaultProps} />);

    expect(screen.getByTestId("priority-badge")).toHaveTextContent(
      "P2 High",
    );
  });

  it("shows assignee", () => {
    render(<NodeDetail {...defaultProps} />);

    expect(screen.getByTestId("assignee")).toHaveTextContent("agent-claude");
  });

  it("renders progress bar", () => {
    render(<NodeDetail {...defaultProps} />);

    const progress = screen.getByTestId("detail-progress");
    expect(progress).toBeInTheDocument();
    expect(progress.querySelector("[role='progressbar']")).toHaveAttribute(
      "aria-valuenow",
      "66",
    );
  });

  it("renders prompt section with Edit button", () => {
    render(<NodeDetail {...defaultProps} />);

    expect(screen.getByTestId("prompt-section")).toBeInTheDocument();
    expect(screen.getByTestId("prompt-display")).toHaveTextContent(
      "Investigate timeout in src/auth/login.go",
    );
    expect(screen.getByTestId("edit-prompt-button")).toBeInTheDocument();
  });

  it("opens prompt editor when Edit is clicked", () => {
    render(<NodeDetail {...defaultProps} />);

    fireEvent.click(screen.getByTestId("edit-prompt-button"));
    expect(screen.getByTestId("prompt-editor")).toBeInTheDocument();
  });

  it("renders children list", () => {
    render(<NodeDetail {...defaultProps} />);

    expect(screen.getByTestId("children-list")).toBeInTheDocument();
    expect(screen.getByText("Make timeout configurable")).toBeInTheDocument();
    expect(screen.getByText("Add retry logic")).toBeInTheDocument();
    expect(screen.getByText("Add loading spinner")).toBeInTheDocument();
  });

  it("renders quick-add bar", () => {
    render(<NodeDetail {...defaultProps} />);

    expect(screen.getByTestId("quick-add-bar")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Add micro issue…")).toBeInTheDocument();
  });

  it("renders context chain", () => {
    render(<NodeDetail {...defaultProps} />);

    expect(screen.getByTestId("context-chain")).toBeInTheDocument();
  });

  it("supports tabbed sections — Description, Activity, Deps", () => {
    render(<NodeDetail {...defaultProps} />);

    expect(screen.getByTestId("tab-bar")).toBeInTheDocument();
    expect(screen.getByTestId("tab-description")).toBeInTheDocument();
    expect(screen.getByTestId("tab-activity")).toBeInTheDocument();
    expect(screen.getByTestId("tab-deps")).toBeInTheDocument();

    // Default tab is Description.
    expect(screen.getByTestId("description-content")).toBeInTheDocument();
  });

  it("switches to Activity tab", () => {
    render(<NodeDetail {...defaultProps} />);

    fireEvent.click(screen.getByTestId("tab-activity"));
    expect(screen.getByTestId("activity-stream")).toBeInTheDocument();
  });

  it("switches to Deps tab", () => {
    render(<NodeDetail {...defaultProps} />);

    fireEvent.click(screen.getByTestId("tab-deps"));
    expect(screen.getByTestId("deps-content")).toBeInTheDocument();
    expect(screen.getByText("No dependencies")).toBeInTheDocument();
  });

  it("shows dependencies when present", () => {
    const deps: Dependency[] = [
      { from_id: "PROJ-1.1.2", to_id: "PROJ-1.1.3", dep_type: "blocks", created_at: "2026-03-08T10:00:00Z" },
    ];

    render(<NodeDetail {...defaultProps} dependencies={deps} />);

    fireEvent.click(screen.getByTestId("tab-deps"));
    expect(screen.getByTestId("dep-item")).toBeInTheDocument();
    expect(screen.getByText("blocks")).toBeInTheDocument();
  });

  it("shows count badge on Activity tab when entries exist", () => {
    const activity = [
      { id: "a1", type: "status_change", author: "agent", text: "Claimed", created_at: "2026-03-08T10:00:00Z" },
      { id: "a2", type: "comment", author: "user", text: "LGTM", created_at: "2026-03-08T11:00:00Z" },
    ];

    render(<NodeDetail {...defaultProps} activityEntries={activity} />);

    expect(screen.getByTestId("tab-activity-count")).toHaveTextContent("2");
  });

  it("shows count badge on Deps tab when dependencies exist", () => {
    const deps: Dependency[] = [
      { from_id: "PROJ-1.1.2", to_id: "PROJ-1.1.3", dep_type: "blocks", created_at: "2026-03-08T10:00:00Z" },
      { from_id: "PROJ-1.1.2", to_id: "PROJ-1.1.4", dep_type: "related", created_at: "2026-03-08T10:00:00Z" },
    ];

    render(<NodeDetail {...defaultProps} dependencies={deps} />);

    expect(screen.getByTestId("tab-deps-count")).toHaveTextContent("2");
  });

  it("hides count badge when count is zero", () => {
    render(<NodeDetail {...defaultProps} activityEntries={[]} dependencies={[]} />);

    expect(screen.queryByTestId("tab-activity-count")).not.toBeInTheDocument();
    expect(screen.queryByTestId("tab-deps-count")).not.toBeInTheDocument();
  });
});
