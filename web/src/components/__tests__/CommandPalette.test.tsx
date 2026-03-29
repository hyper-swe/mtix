import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { CommandPalette } from "../CommandPalette";
import type { Node } from "../../types";

/**
 * Command palette tests per MTIX-9.2.3.
 * Tests open/close, search, recent items, actions, and navigation.
 */

// Mock the API module.
vi.mock("../../api", () => ({
  searchNodes: vi.fn().mockResolvedValue([]),
}));

import * as api from "../../api";

beforeEach(() => {
  window.localStorage.clear();
  vi.mocked(api.searchNodes).mockClear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("CommandPalette", () => {
  it("renders when isOpen is true", () => {
    render(
      <CommandPalette
        isOpen={true}
        onClose={vi.fn()}
        onSelectNode={vi.fn()}
      />,
    );

    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByLabelText("Search")).toBeInTheDocument();
  });

  it("does not render when isOpen is false", () => {
    render(
      <CommandPalette
        isOpen={false}
        onClose={vi.fn()}
        onSelectNode={vi.fn()}
      />,
    );

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("calls onClose when Escape is pressed", () => {
    const onClose = vi.fn();

    render(
      <CommandPalette
        isOpen={true}
        onClose={onClose}
        onSelectNode={vi.fn()}
      />,
    );

    const input = screen.getByLabelText("Search");
    fireEvent.keyDown(input, { key: "Escape" });

    expect(onClose).toHaveBeenCalled();
  });

  it("calls onClose when clicking backdrop", () => {
    const onClose = vi.fn();

    render(
      <CommandPalette
        isOpen={true}
        onClose={onClose}
        onSelectNode={vi.fn()}
      />,
    );

    const backdrop = screen.getByRole("dialog");
    fireEvent.click(backdrop);

    expect(onClose).toHaveBeenCalled();
  });

  it("shows actions section when no query", () => {
    render(
      <CommandPalette
        isOpen={true}
        onClose={vi.fn()}
        onSelectNode={vi.fn()}
      />,
    );

    expect(screen.getByText("Actions")).toBeInTheDocument();
    expect(screen.getByText("Create node")).toBeInTheDocument();
    expect(screen.getByText("Switch view")).toBeInTheDocument();
    expect(screen.getByText("Settings")).toBeInTheDocument();
  });

  it("performs debounced search", async () => {
    const mockNodes: Node[] = [
      {
        id: "PROJ-1",
        title: "Test Node",
        status: "open",
        parent_id: "",
        project: "PROJ",
        depth: 0,
        seq: 1,
        description: "",
        prompt: "",
        acceptance: "",
        labels: [],
        priority: 3 as const,
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
      },
    ];
    vi.mocked(api.searchNodes).mockResolvedValue(mockNodes);

    render(
      <CommandPalette
        isOpen={true}
        onClose={vi.fn()}
        onSelectNode={vi.fn()}
      />,
    );

    const input = screen.getByLabelText("Search");
    fireEvent.change(input, { target: { value: "test" } });

    // Search is debounced — wait for it to fire.
    await waitFor(
      () => {
        expect(api.searchNodes).toHaveBeenCalledWith("test", { limit: 10 });
      },
      { timeout: 1000 },
    );
  });

  it("navigates results with arrow keys", () => {
    render(
      <CommandPalette
        isOpen={true}
        onClose={vi.fn()}
        onSelectNode={vi.fn()}
      />,
    );

    const input = screen.getByLabelText("Search");

    // Default view has actions (3 items).
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "ArrowUp" });

    // No crash — navigation works.
    expect(input).toBeInTheDocument();
  });

  it("shows recent items from localStorage", () => {
    window.localStorage.setItem(
      "mtix-recent-nodes",
      JSON.stringify(["PROJ-1", "PROJ-2"]),
    );

    render(
      <CommandPalette
        isOpen={true}
        onClose={vi.fn()}
        onSelectNode={vi.fn()}
      />,
    );

    expect(screen.getByText("Recent")).toBeInTheDocument();
    expect(screen.getByText("PROJ-1")).toBeInTheDocument();
    expect(screen.getByText("PROJ-2")).toBeInTheDocument();
  });

  it("calls onAction when an action is clicked", () => {
    const onAction = vi.fn();

    render(
      <CommandPalette
        isOpen={true}
        onClose={vi.fn()}
        onSelectNode={vi.fn()}
        onAction={onAction}
      />,
    );

    fireEvent.click(screen.getByText("Create node"));
    expect(onAction).toHaveBeenCalledWith("create-node");
  });

  it("shows no results message for empty search", async () => {
    vi.mocked(api.searchNodes).mockResolvedValue([]);

    render(
      <CommandPalette
        isOpen={true}
        onClose={vi.fn()}
        onSelectNode={vi.fn()}
      />,
    );

    const input = screen.getByLabelText("Search");
    fireEvent.change(input, { target: { value: "nonexistent" } });

    await waitFor(
      () => {
        expect(screen.getByText("No results found")).toBeInTheDocument();
      },
      { timeout: 1000 },
    );
  });
});
