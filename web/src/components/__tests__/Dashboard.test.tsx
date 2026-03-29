import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { Dashboard } from "../Dashboard";
import type { Stats } from "../../types";
import type { StoryProgress, AgentProductivity, BurndownPoint } from "../Dashboard";

/**
 * Dashboard tests per MTIX-9.4.1.
 * Tests overall progress, status breakdown, burndown chart,
 * per-story progress, agent productivity, and time range selector.
 */

const mockStats: Stats = {
  total_nodes: 100,
  by_status: {
    open: 20,
    in_progress: 15,
    blocked: 5,
    done: 50,
    deferred: 3,
    cancelled: 2,
    invalidated: 5,
  },
  by_priority: { "1": 10, "2": 20, "3": 40, "4": 20, "5": 10 },
  by_type: { issue: 60, epic: 20, story: 10, micro: 10 },
  progress: 0.5,
  scope_id: "PROJ",
};

const mockStories: StoryProgress[] = [
  { id: "PROJ-1", title: "User Auth", progress: 0.8, status: "in_progress", childCount: 10 },
  { id: "PROJ-2", title: "Payments", progress: 0.3, status: "open", childCount: 20 },
];

const mockAgentStats: AgentProductivity[] = [
  { agentId: "agent-claude", completed: 25, inProgress: 2 },
  { agentId: "agent-gpt4", completed: 15, inProgress: 0 },
];

const mockBurndown: BurndownPoint[] = Array.from({ length: 30 }, (_, i) => ({
  date: `2026-02-${String(i + 1).padStart(2, "0")}`,
  completed: i * 2,
  total: 100,
}));

const defaultProps = {
  stats: mockStats,
  stories: mockStories,
  agentStats: mockAgentStats,
  burndown: mockBurndown,
  onNavigate: vi.fn(),
};

describe("Dashboard", () => {
  it("renders overall progress", () => {
    render(<Dashboard {...defaultProps} />);

    expect(screen.getByTestId("overall-progress")).toBeInTheDocument();
    expect(screen.getByText("100 nodes total")).toBeInTheDocument();
  });

  it("renders status breakdown with stacked bar", () => {
    render(<Dashboard {...defaultProps} />);

    expect(screen.getByTestId("status-breakdown")).toBeInTheDocument();
    expect(screen.getByTestId("stacked-bar")).toBeInTheDocument();
    // Legend entries.
    expect(screen.getByTestId("legend-done")).toBeInTheDocument();
    expect(screen.getByTestId("legend-open")).toBeInTheDocument();
  });

  it("renders burndown chart", () => {
    render(<Dashboard {...defaultProps} />);

    expect(screen.getByTestId("burndown-section")).toBeInTheDocument();
    expect(screen.getByTestId("burndown-chart")).toBeInTheDocument();
    expect(screen.getAllByTestId("burndown-bar").length).toBeGreaterThan(0);
  });

  it("renders per-story progress", () => {
    render(<Dashboard {...defaultProps} />);

    expect(screen.getByTestId("story-progress")).toBeInTheDocument();
    expect(screen.getByTestId("story-PROJ-1")).toBeInTheDocument();
    expect(screen.getByText("User Auth")).toBeInTheDocument();
  });

  it("renders agent productivity", () => {
    render(<Dashboard {...defaultProps} />);

    expect(screen.getByTestId("agent-productivity")).toBeInTheDocument();
    expect(screen.getByTestId("agent-stat-agent-claude")).toBeInTheDocument();
    expect(screen.getByText("25 done")).toBeInTheDocument();
  });

  it("time range selector works", () => {
    render(<Dashboard {...defaultProps} />);

    // Default is 30d, switch to 7d.
    fireEvent.click(screen.getByTestId("range-7d"));
    // Burndown should still render (with fewer bars).
    expect(screen.getByTestId("burndown-chart")).toBeInTheDocument();
  });

  it("navigates to story when clicked", () => {
    const onNavigate = vi.fn();
    render(<Dashboard {...defaultProps} onNavigate={onNavigate} />);

    fireEvent.click(screen.getByTestId("story-PROJ-1"));
    expect(onNavigate).toHaveBeenCalledWith("PROJ-1");
  });

  it("handles empty burndown data", () => {
    render(<Dashboard {...defaultProps} burndown={[]} />);

    expect(screen.getByText("No data for this period")).toBeInTheDocument();
  });
});
