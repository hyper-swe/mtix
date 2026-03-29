import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";

/**
 * MainContent tests for StaleView and AgentsView per MTIX-16.5 and MTIX-16.6.
 * Mocks API calls and NavigationContext.
 */

// Mock API module.
vi.mock("../../api", () => ({
  getStaleEntries: vi.fn(),
  listNodes: vi.fn(),
}));

// Mock NavigationContext.
vi.mock("../../contexts/NavigationContext", () => ({
  useNavigation: vi.fn(),
}));

import { MainContent } from "../MainContent";
import * as api from "../../api";
import { useNavigation } from "../../contexts/NavigationContext";

function makeNodeStore() {
  return {
    nodes: new Map<string, never>(),
    expanded: new Set<string>(),
    selectedId: null,
    rootIds: [] as string[],
    childrenMap: new Map<string, string[]>(),
    loadedChildren: new Set<string>(),
    loadingChildren: new Set<string>(),
    loading: false,
    hideDone: false,
    setHideDone: vi.fn(),
    loadRoots: vi.fn(),
    loadChildren: vi.fn(),
    toggleExpand: vi.fn(),
    selectNode: vi.fn(),
    updateNode: vi.fn(),
    addNode: vi.fn(),
    removeNode: vi.fn(),
    flatTree: vi.fn().mockReturnValue([]),
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("StaleView", () => {
  it("renders empty state when no stale agents", async () => {
    vi.mocked(useNavigation).mockReturnValue({
      view: "stale",
      selectedNodeId: null,
      selectNode: vi.fn(),
      navigateTo: vi.fn(),
      goBack: vi.fn(),
    });
    vi.mocked(api.getStaleEntries).mockResolvedValue({ agents: [], total: 0 });

    render(<MainContent nodeStore={makeNodeStore()} />);

    await waitFor(() => {
      expect(screen.getByText("No stale items")).toBeInTheDocument();
    });
  });

  it("renders stale agents when present", async () => {
    vi.mocked(useNavigation).mockReturnValue({
      view: "stale",
      selectedNodeId: null,
      selectNode: vi.fn(),
      navigateTo: vi.fn(),
      goBack: vi.fn(),
    });
    vi.mocked(api.getStaleEntries).mockResolvedValue({
      agents: ["agent-claude", "agent-gpt"],
      total: 2,
    });

    render(<MainContent nodeStore={makeNodeStore()} />);

    await waitFor(() => {
      expect(screen.getByText("Agent: agent-claude")).toBeInTheDocument();
      expect(screen.getByText("Agent: agent-gpt")).toBeInTheDocument();
      expect(screen.getByText("2 stale agents detected")).toBeInTheDocument();
    });
  });

  it("handles API error gracefully", async () => {
    vi.mocked(useNavigation).mockReturnValue({
      view: "stale",
      selectedNodeId: null,
      selectNode: vi.fn(),
      navigateTo: vi.fn(),
      goBack: vi.fn(),
    });
    vi.mocked(api.getStaleEntries).mockRejectedValue(new Error("Network error"));

    render(<MainContent nodeStore={makeNodeStore()} />);

    await waitFor(() => {
      expect(screen.getByText("No stale items")).toBeInTheDocument();
    });
  });
});

describe("AgentsView", () => {
  it("renders empty state when no active agents", async () => {
    vi.mocked(useNavigation).mockReturnValue({
      view: "agents",
      selectedNodeId: null,
      selectNode: vi.fn(),
      navigateTo: vi.fn(),
      goBack: vi.fn(),
    });
    vi.mocked(api.listNodes).mockResolvedValue({ nodes: [], total: 0, has_more: false });

    render(<MainContent nodeStore={makeNodeStore()} />);

    await waitFor(() => {
      expect(screen.getByText("No active agents")).toBeInTheDocument();
    });
  });

  it("renders active agents from in_progress nodes", async () => {
    vi.mocked(useNavigation).mockReturnValue({
      view: "agents",
      selectedNodeId: null,
      selectNode: vi.fn(),
      navigateTo: vi.fn(),
      goBack: vi.fn(),
    });
    vi.mocked(api.listNodes).mockResolvedValue({
      nodes: [
        {
          id: "PROJ-1",
          title: "Fix bug",
          assignee: "agent-claude",
          status: "in_progress",
          parent_id: "",
          project: "PROJ",
          depth: 0,
          seq: 1,
          description: "",
          prompt: "",
          acceptance: "",
          labels: [],
          priority: 3,
          node_type: "issue",
          issue_type: "bug",
          creator: "",
          agent_state: "working",
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
      ],
      total: 1,
      has_more: false,
    });

    render(<MainContent nodeStore={makeNodeStore()} />);

    await waitFor(() => {
      expect(screen.getByText("agent-claude")).toBeInTheDocument();
      expect(screen.getByText("working")).toBeInTheDocument();
      expect(screen.getByText("1 active agent")).toBeInTheDocument();
    });
  });

  it("handles API error gracefully", async () => {
    vi.mocked(useNavigation).mockReturnValue({
      view: "agents",
      selectedNodeId: null,
      selectNode: vi.fn(),
      navigateTo: vi.fn(),
      goBack: vi.fn(),
    });
    vi.mocked(api.listNodes).mockRejectedValue(new Error("Fail"));

    render(<MainContent nodeStore={makeNodeStore()} />);

    await waitFor(() => {
      expect(screen.getByText("No active agents")).toBeInTheDocument();
    });
  });
});

describe("MainContent routing", () => {
  it("renders stale view title", async () => {
    vi.mocked(useNavigation).mockReturnValue({
      view: "stale",
      selectedNodeId: null,
      selectNode: vi.fn(),
      navigateTo: vi.fn(),
      goBack: vi.fn(),
    });
    vi.mocked(api.getStaleEntries).mockResolvedValue({ agents: [], total: 0 });

    render(<MainContent nodeStore={makeNodeStore()} />);

    expect(screen.getByText("Stale Board")).toBeInTheDocument();
  });

  it("renders agents view title", async () => {
    vi.mocked(useNavigation).mockReturnValue({
      view: "agents",
      selectedNodeId: null,
      selectNode: vi.fn(),
      navigateTo: vi.fn(),
      goBack: vi.fn(),
    });
    vi.mocked(api.listNodes).mockResolvedValue({ nodes: [], total: 0, has_more: false });

    render(<MainContent nodeStore={makeNodeStore()} />);

    expect(screen.getByText("Agent Activity")).toBeInTheDocument();
  });
});
