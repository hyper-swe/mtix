import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { StaleBoard } from "../StaleBoard";
import type { StaleEntry, Node } from "../../types";

/**
 * Stale board tests per MTIX-9.4.4.
 * Tests 4 staleness categories, quick actions, empty state,
 * and real-time updates.
 */

function makeNode(overrides: Partial<Node> = {}): Node {
  return {
    id: "PROJ-1.1.2",
    parent_id: "PROJ-1.1",
    project: "PROJ",
    depth: 2,
    seq: 2,
    title: "Fix timeout bug",
    description: "",
    prompt: "",
    acceptance: "",
    labels: [],
    priority: 3 as const,
    status: "in_progress",
    node_type: "issue",
    issue_type: "task",
    creator: "",
    assignee: "agent-claude",
    agent_state: "working",
    weight: 1,
    progress: 0.5,
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

const mockEntries: StaleEntry[] = [
  {
    node: makeNode({ id: "PROJ-1", status: "in_progress" }),
    reason: "in_progress_too_long",
    time_since_activity: 7200,
    assigned_agent: "agent-claude",
  },
  {
    node: makeNode({ id: "PROJ-2", status: "in_progress" }),
    reason: "stale_heartbeat",
    time_since_activity: 600,
    assigned_agent: "agent-gpt4",
  },
  {
    node: makeNode({ id: "PROJ-3", status: "invalidated" }),
    reason: "invalidated",
    time_since_activity: 3600,
    assigned_agent: null,
  },
  {
    node: makeNode({ id: "PROJ-4", status: "blocked" }),
    reason: "blocked_not_unblocked",
    time_since_activity: 900,
    assigned_agent: null,
  },
];

describe("StaleBoard", () => {
  it("shows in_progress too long nodes", () => {
    render(
      <StaleBoard
        entries={mockEntries}
        onAction={vi.fn()}
        onNavigate={vi.fn()}
      />,
    );

    expect(
      screen.getByTestId("stale-category-in_progress_too_long"),
    ).toBeInTheDocument();
    expect(screen.getByTestId("stale-item-PROJ-1")).toBeInTheDocument();
  });

  it("shows stale heartbeat nodes", () => {
    render(
      <StaleBoard
        entries={mockEntries}
        onAction={vi.fn()}
        onNavigate={vi.fn()}
      />,
    );

    expect(
      screen.getByTestId("stale-category-stale_heartbeat"),
    ).toBeInTheDocument();
    expect(screen.getByTestId("stale-item-PROJ-2")).toBeInTheDocument();
  });

  it("shows invalidated nodes awaiting review", () => {
    render(
      <StaleBoard
        entries={mockEntries}
        onAction={vi.fn()}
        onNavigate={vi.fn()}
      />,
    );

    expect(
      screen.getByTestId("stale-category-invalidated"),
    ).toBeInTheDocument();
    expect(screen.getByTestId("stale-item-PROJ-3")).toBeInTheDocument();
  });

  it("shows blocked not unblocked nodes", () => {
    render(
      <StaleBoard
        entries={mockEntries}
        onAction={vi.fn()}
        onNavigate={vi.fn()}
      />,
    );

    expect(
      screen.getByTestId("stale-category-blocked_not_unblocked"),
    ).toBeInTheDocument();
    expect(screen.getByTestId("stale-item-PROJ-4")).toBeInTheDocument();
    // Blocked nodes have "Unblock" action.
    expect(
      screen.getByTestId("action-unblock-PROJ-4"),
    ).toBeInTheDocument();
  });

  it("triggers quick actions", () => {
    const onAction = vi.fn();
    render(
      <StaleBoard
        entries={mockEntries}
        onAction={onAction}
        onNavigate={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByTestId("action-reassign-PROJ-1"));
    expect(onAction).toHaveBeenCalledWith("PROJ-1", "reassign");

    fireEvent.click(screen.getByTestId("action-cancel-PROJ-2"));
    expect(onAction).toHaveBeenCalledWith("PROJ-2", "cancel");

    fireEvent.click(screen.getByTestId("action-unblock-PROJ-4"));
    expect(onAction).toHaveBeenCalledWith("PROJ-4", "unblock");
  });

  it("navigates when node link is clicked", () => {
    const onNavigate = vi.fn();
    render(
      <StaleBoard
        entries={mockEntries}
        onAction={vi.fn()}
        onNavigate={onNavigate}
      />,
    );

    fireEvent.click(screen.getByTestId("stale-link-PROJ-1"));
    expect(onNavigate).toHaveBeenCalledWith("PROJ-1");
  });

  it("shows empty state when no stale items", () => {
    render(
      <StaleBoard
        entries={[]}
        onAction={vi.fn()}
        onNavigate={vi.fn()}
      />,
    );

    expect(screen.getByText("No stale items")).toBeInTheDocument();
    expect(
      screen.getByText("Everything is running smoothly"),
    ).toBeInTheDocument();
  });

  it("shows staleness duration", () => {
    render(
      <StaleBoard
        entries={mockEntries}
        onAction={vi.fn()}
        onNavigate={vi.fn()}
      />,
    );

    const durations = screen.getAllByTestId("stale-duration");
    expect(durations.length).toBe(4);
    // Categories sorted: stale_heartbeat (600s=10m), in_progress (7200s=2h),
    // invalidated (3600s=1h), blocked (900s=15m).
    expect(durations[0]).toHaveTextContent("10m");
    expect(durations[1]).toHaveTextContent("2h");
  });
});
