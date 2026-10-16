/** Component regressions for activity pagination after concurrent writes. */
import { beforeEach, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { NodeDetailView } from "../NodeDetailView";
import { useNodeStore } from "../../hooks/useNodeStore";
import type { ActivityEntry, Node } from "../../types";
import * as api from "../../api";

vi.mock("../../api", () => ({
  getNode: vi.fn(),
  getChildren: vi.fn(),
  getActivity: vi.fn(),
  getDependencies: vi.fn(),
}));

const node: Node = {
  id: "PROJ-1", parent_id: "", project: "PROJ", depth: 0, seq: 1,
  title: "Activity pagination", description: "", prompt: "", acceptance: "",
  labels: [], priority: 3, status: "open", node_type: "epic", issue_type: "task",
  creator: "writer", assignee: "", agent_state: "idle", weight: 1, progress: 0,
  content_hash: "", child_count: 0, created_at: null, updated_at: null,
  closed_at: null, defer_until: null, deleted_at: null, annotations: [],
};

function entry(index: number): ActivityEntry {
  return {
    id: `entry-${index}`, type: "comment", author: "writer",
    text: `Comment ${index}`, created_at: "2026-10-14T12:00:00Z",
  };
}

function entries(start: number, count: number): ActivityEntry[] {
  return Array.from({ length: count }, (_, i) => entry(start + i));
}

function View({ nodeId = node.id }: { nodeId?: string }) {
  const store = useNodeStore();
  return <NodeDetailView nodeId={nodeId} nodeStore={store} onNavigate={vi.fn()} />;
}

async function openActivity() {
  fireEvent.click(await screen.findByTestId("tab-activity"));
  expect(screen.getAllByTestId("activity-entry")).toHaveLength(20);
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.getNode).mockResolvedValue(node);
  vi.mocked(api.getChildren).mockResolvedValue([]);
  vi.mocked(api.getDependencies).mockResolvedValue([]);
});

it("shows a boundary entry only once when a new write shifts the next page", async () => {
  vi.mocked(api.getActivity).mockResolvedValueOnce(entries(0, 20))
    .mockResolvedValueOnce(entries(19, 3));
  render(<View />);
  await openActivity();
  fireEvent.click(screen.getByTestId("load-more"));
  await screen.findByText("Comment 21");
  expect(screen.getAllByText("Comment 19")).toHaveLength(1);
  expect(screen.getAllByTestId("activity-entry")).toHaveLength(22);
  expect(screen.getByTestId("tab-activity-count")).toHaveTextContent("22");
  expect(api.getActivity).toHaveBeenLastCalledWith(node.id, { limit: 20, offset: 20 });
  expect(screen.queryByTestId("load-more")).not.toBeInTheDocument();
});

it("advances past a wholly overlapping page to reach older entries", async () => {
  vi.mocked(api.getActivity).mockResolvedValueOnce(entries(0, 20))
    .mockResolvedValueOnce(entries(0, 20)).mockResolvedValueOnce(entries(20, 1));
  render(<View />);
  await openActivity();
  fireEvent.click(screen.getByTestId("load-more"));
  await waitFor(() => expect(screen.getByTestId("load-more")).not.toBeDisabled());
  expect(screen.getAllByTestId("activity-entry")).toHaveLength(20);
  fireEvent.click(screen.getByTestId("load-more"));
  await screen.findByText("Comment 20");
  expect(api.getActivity).toHaveBeenLastCalledWith(node.id, { limit: 20, offset: 40 });
  expect(screen.getAllByTestId("activity-entry")).toHaveLength(21);
});

it("deduplicates within a page by ID while preserving an entry without an ID", async () => {
  const repeated = entry(20);
  const sameText = { ...entry(21), text: repeated.text };
  const unidentified = { ...entry(22), id: "" };
  vi.mocked(api.getActivity).mockResolvedValueOnce(entries(0, 20))
    .mockResolvedValueOnce([repeated, repeated, sameText, unidentified]);
  render(<View />);
  await openActivity();
  fireEvent.click(screen.getByTestId("load-more"));
  await screen.findByText("Comment 22");
  expect(screen.getAllByText("Comment 20")).toHaveLength(2);
  expect(screen.getAllByTestId("activity-entry")).toHaveLength(23);
});

it("retries a failed page at the same offset", async () => {
  vi.mocked(api.getActivity).mockResolvedValueOnce(entries(0, 20))
    .mockRejectedValueOnce(new Error("Unavailable")).mockResolvedValueOnce(entries(20, 1));
  render(<View />);
  await openActivity();
  fireEvent.click(screen.getByTestId("load-more"));
  await waitFor(() => expect(screen.getByTestId("load-more")).not.toBeDisabled());
  expect(screen.getAllByTestId("activity-entry")).toHaveLength(20);
  fireEvent.click(screen.getByTestId("load-more"));
  await screen.findByText("Comment 20");
  expect(api.getActivity).toHaveBeenNthCalledWith(2, node.id, { limit: 20, offset: 20 });
  expect(api.getActivity).toHaveBeenNthCalledWith(3, node.id, { limit: 20, offset: 20 });
});

it("resets page progress when navigating to another node", async () => {
  vi.mocked(api.getActivity).mockResolvedValueOnce(entries(0, 20))
    .mockResolvedValueOnce(entries(19, 20)).mockResolvedValueOnce(entries(100, 20))
    .mockResolvedValueOnce(entries(120, 1));
  const view = render(<View />);
  await openActivity();
  fireEvent.click(screen.getByTestId("load-more"));
  await screen.findByText("Comment 38");
  view.rerender(<View nodeId="PROJ-2" />);
  await openActivity();
  expect(screen.queryByText("Comment 0")).not.toBeInTheDocument();
  fireEvent.click(screen.getByTestId("load-more"));
  await screen.findByText("Comment 120");
  expect(api.getActivity).toHaveBeenLastCalledWith("PROJ-2", { limit: 20, offset: 20 });
});
