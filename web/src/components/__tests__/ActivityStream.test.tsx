import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ActivityStream } from "../ActivityStream";
import type { ActivityEntry } from "../../types";

/**
 * Activity stream tests per MTIX-9.3.4.
 * Tests entry rendering, pagination, load more, filtering,
 * relative timestamps, and agent vs human distinction.
 */

function makeEntry(overrides: Partial<ActivityEntry> = {}): ActivityEntry {
  return {
    id: "act-1",
    type: "status_change",
    author: "vimal",
    text: "Changed status to in_progress",
    created_at: new Date(Date.now() - 120000).toISOString(), // 2 min ago
    ...overrides,
  };
}

const mockEntries: ActivityEntry[] = [
  makeEntry({
    id: "act-1",
    type: "status_change",
    author: "vimal",
    text: "Changed status to in_progress",
  }),
  makeEntry({
    id: "act-2",
    type: "comment",
    author: "agent-claude",
    text: "Using exponential backoff",
  }),
  makeEntry({
    id: "act-3",
    type: "agent_claim",
    author: "agent-claude",
    text: "Claimed node PROJ-42.1.3.2",
  }),
  makeEntry({
    id: "act-4",
    type: "prompt_edit",
    author: "vimal",
    text: "Updated prompt text",
  }),
];

describe("ActivityStream", () => {
  it("renders activity entries", () => {
    render(
      <ActivityStream
        entries={mockEntries}
        hasMore={false}
        onLoadMore={vi.fn()}
      />,
    );

    expect(screen.getByTestId("activity-stream")).toBeInTheDocument();
    expect(screen.getAllByTestId("activity-entry")).toHaveLength(4);
  });

  it("shows Load more button when hasMore is true", () => {
    render(
      <ActivityStream
        entries={mockEntries}
        hasMore={true}
        onLoadMore={vi.fn()}
      />,
    );

    expect(screen.getByTestId("load-more")).toBeInTheDocument();
    expect(screen.getByText("Load more")).toBeInTheDocument();
  });

  it("calls onLoadMore when Load more is clicked", () => {
    const onLoadMore = vi.fn();

    render(
      <ActivityStream
        entries={mockEntries}
        hasMore={true}
        onLoadMore={onLoadMore}
      />,
    );

    fireEvent.click(screen.getByTestId("load-more"));
    expect(onLoadMore).toHaveBeenCalled();
  });

  it("filters by activity type", () => {
    render(
      <ActivityStream
        entries={mockEntries}
        hasMore={false}
        onLoadMore={vi.fn()}
      />,
    );

    // Click "Status" filter.
    fireEvent.click(screen.getByTestId("filter-status"));
    expect(screen.getAllByTestId("activity-entry")).toHaveLength(1);

    // Click "Agent" filter.
    fireEvent.click(screen.getByTestId("filter-agent"));
    expect(screen.getAllByTestId("activity-entry")).toHaveLength(1);

    // Click "All" to reset.
    fireEvent.click(screen.getByTestId("filter-all"));
    expect(screen.getAllByTestId("activity-entry")).toHaveLength(4);
  });

  it("shows relative timestamps", () => {
    render(
      <ActivityStream
        entries={[makeEntry({ created_at: new Date(Date.now() - 120000).toISOString() })]}
        hasMore={false}
        onLoadMore={vi.fn()}
      />,
    );

    expect(screen.getByTestId("activity-timestamp").textContent).toBe(
      "2 min ago",
    );
  });

  it("shows full timestamp on hover (title attribute)", () => {
    const date = new Date(Date.now() - 120000).toISOString();

    render(
      <ActivityStream
        entries={[makeEntry({ created_at: date })]}
        hasMore={false}
        onLoadMore={vi.fn()}
      />,
    );

    const timestamp = screen.getByTestId("activity-timestamp");
    expect(timestamp.title).toBeTruthy();
  });

  it("visually distinguishes agent vs human entries", () => {
    render(
      <ActivityStream
        entries={mockEntries}
        hasMore={false}
        onLoadMore={vi.fn()}
      />,
    );

    const authors = screen.getAllByTestId("activity-author");
    // agent-claude entries should use accent color (checked via style).
    const agentAuthor = authors.find((a) => a.textContent === "agent-claude");
    expect(agentAuthor).toBeTruthy();
    expect(agentAuthor?.style.color).toBe("var(--color-accent)");

    // Human entries should use primary text color.
    const humanAuthor = authors.find((a) => a.textContent === "vimal");
    expect(humanAuthor).toBeTruthy();
    expect(humanAuthor?.style.color).toBe("var(--color-text-primary)");
  });

  it("shows empty state when no entries", () => {
    render(
      <ActivityStream
        entries={[]}
        hasMore={false}
        onLoadMore={vi.fn()}
      />,
    );

    expect(screen.getByText("No activity")).toBeInTheDocument();
  });

  it("shows Loading… when loading more", () => {
    render(
      <ActivityStream
        entries={mockEntries}
        hasMore={true}
        onLoadMore={vi.fn()}
        loading={true}
      />,
    );

    expect(screen.getByText("Loading…")).toBeInTheDocument();
  });
});
