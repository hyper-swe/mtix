import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { AgentDashboard } from "../AgentDashboard";
import type { AgentInfo } from "../../types";

/**
 * Agent dashboard tests per MTIX-9.4.2.
 * Tests agent cards, working/stuck/idle states, sort order,
 * heartbeat staleness, and recent actions.
 */

function makeAgent(overrides: Partial<AgentInfo> = {}): AgentInfo {
  return {
    agent_id: "agent-claude",
    state: "working",
    current_node_id: "PROJ-1.1.2",
    current_node_title: "Fix timeout bug",
    session_started_at: new Date(Date.now() - 1380000).toISOString(), // 23 min ago
    last_heartbeat: new Date(Date.now() - 12000).toISOString(), // 12s ago
    nodes_completed: 5,
    recent_actions: [
      {
        id: "act-1",
        type: "agent_done",
        author: "agent-claude",
        text: "Done: Fix retry context cancel",
        created_at: new Date(Date.now() - 60000).toISOString(),
      },
    ],
    ...overrides,
  };
}

const workingAgent = makeAgent();
const stuckAgent = makeAgent({
  agent_id: "agent-gpt4",
  state: "stuck",
  last_heartbeat: new Date(Date.now() - 600000).toISOString(), // 10 min ago
});
const idleAgent = makeAgent({
  agent_id: "agent-gemini",
  state: "idle",
  current_node_id: null,
  current_node_title: null,
  recent_actions: [],
});

describe("AgentDashboard", () => {
  it("renders agent cards", () => {
    render(
      <AgentDashboard
        agents={[workingAgent]}
        onNavigate={vi.fn()}
      />,
    );

    expect(screen.getByTestId("agent-dashboard")).toBeInTheDocument();
    expect(
      screen.getByTestId("agent-card-agent-claude"),
    ).toBeInTheDocument();
  });

  it("shows working agent details", () => {
    render(
      <AgentDashboard
        agents={[workingAgent]}
        onNavigate={vi.fn()}
      />,
    );

    expect(screen.getByTestId("agent-name")).toHaveTextContent("agent-claude");
    expect(screen.getByTestId("agent-status")).toHaveTextContent("working");
    expect(screen.getByTestId("current-work")).toBeInTheDocument();
  });

  it("highlights stuck agent with red border and badge", () => {
    render(
      <AgentDashboard
        agents={[stuckAgent]}
        onNavigate={vi.fn()}
      />,
    );

    const card = screen.getByTestId("agent-card-agent-gpt4");
    expect(card.style.borderColor).toBe("var(--color-status-blocked)");
    expect(screen.getByTestId("stuck-badge")).toBeInTheDocument();
  });

  it("grays out idle agent", () => {
    render(
      <AgentDashboard
        agents={[idleAgent]}
        onNavigate={vi.fn()}
      />,
    );

    const card = screen.getByTestId("agent-card-agent-gemini");
    expect(card.style.opacity).toBe("0.6");
    expect(screen.getByTestId("idle-message")).toBeInTheDocument();
  });

  it("sorts agents: working → stuck → idle", () => {
    render(
      <AgentDashboard
        agents={[idleAgent, stuckAgent, workingAgent]}
        onNavigate={vi.fn()}
      />,
    );

    const cards = screen.getAllByTestId(/^agent-card-/);
    expect(cards[0]).toHaveAttribute(
      "data-testid",
      "agent-card-agent-claude",
    );
    expect(cards[1]).toHaveAttribute(
      "data-testid",
      "agent-card-agent-gpt4",
    );
    expect(cards[2]).toHaveAttribute(
      "data-testid",
      "agent-card-agent-gemini",
    );
  });

  it("shows stale heartbeat indicator", () => {
    render(
      <AgentDashboard
        agents={[stuckAgent]}
        onNavigate={vi.fn()}
      />,
    );

    const heartbeat = screen.getByTestId("heartbeat");
    expect(heartbeat.style.color).toBe("var(--color-status-blocked)");
  });

  it("shows recent actions timeline", () => {
    render(
      <AgentDashboard
        agents={[workingAgent]}
        onNavigate={vi.fn()}
      />,
    );

    expect(screen.getAllByTestId("agent-action")).toHaveLength(1);
    expect(
      screen.getByText("Done: Fix retry context cancel"),
    ).toBeInTheDocument();
  });

  it("navigates when current work is clicked", () => {
    const onNavigate = vi.fn();
    render(
      <AgentDashboard agents={[workingAgent]} onNavigate={onNavigate} />,
    );

    fireEvent.click(screen.getByTestId("current-work"));
    expect(onNavigate).toHaveBeenCalledWith("PROJ-1.1.2");
  });

  it("shows empty state when no agents", () => {
    render(
      <AgentDashboard agents={[]} onNavigate={vi.fn()} />,
    );

    expect(screen.getByText("No agents registered")).toBeInTheDocument();
  });
});
