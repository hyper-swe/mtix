import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ContextChain } from "../ContextChain";
import type { ContextEntry } from "../../types";

/**
 * Context chain tests per MTIX-9.3.3.
 * Tests ancestry path, level indicators, current marker, click navigation,
 * source attribution, and collapsible expand.
 */

const mockChain: ContextEntry[] = [
  {
    id: "PROJ-1",
    title: "User Auth",
    status: "in_progress",
    prompt: "[HUMAN-AUTHORED]\nAuthentication story",
    acceptance: "",
    depth: 0,
  },
  {
    id: "PROJ-1.1",
    title: "Login flow",
    status: "in_progress",
    prompt: "Implement login flow",
    acceptance: "",
    depth: 1,
  },
  {
    id: "PROJ-1.1.2",
    title: "Fix timeout bug",
    status: "in_progress",
    prompt: "Fix the timeout issue in auth",
    acceptance: "",
    depth: 2,
  },
];

describe("ContextChain", () => {
  it("shows ancestry path from root to current node", () => {
    render(
      <ContextChain
        chain={mockChain}
        currentNodeId="PROJ-1.1.2"
        onNavigate={vi.fn()}
      />,
    );

    expect(screen.getByTestId("context-chain")).toBeInTheDocument();
    expect(screen.getByText("User Auth")).toBeInTheDocument();
    expect(screen.getByText("Login flow")).toBeInTheDocument();
    expect(screen.getByText("Fix timeout bug")).toBeInTheDocument();
  });

  it("shows level indicators (S/E/I)", () => {
    render(
      <ContextChain
        chain={mockChain}
        currentNodeId="PROJ-1.1.2"
        onNavigate={vi.fn()}
      />,
    );

    expect(screen.getByTestId("level-S")).toBeInTheDocument();
    expect(screen.getByTestId("level-E")).toBeInTheDocument();
    expect(screen.getByTestId("level-I")).toBeInTheDocument();
  });

  it("marks current node with ▶ THIS", () => {
    render(
      <ContextChain
        chain={mockChain}
        currentNodeId="PROJ-1.1.2"
        onNavigate={vi.fn()}
      />,
    );

    expect(screen.getByTestId("current-marker")).toBeInTheDocument();
    expect(screen.getByTestId("current-marker").textContent).toBe("▶ THIS");
  });

  it("ancestors are clickable for navigation", () => {
    const onNavigate = vi.fn();

    render(
      <ContextChain
        chain={mockChain}
        currentNodeId="PROJ-1.1.2"
        onNavigate={onNavigate}
      />,
    );

    fireEvent.click(screen.getByTestId("chain-link-PROJ-1"));
    expect(onNavigate).toHaveBeenCalledWith("PROJ-1");

    fireEvent.click(screen.getByTestId("chain-link-PROJ-1.1"));
    expect(onNavigate).toHaveBeenCalledWith("PROJ-1.1");
  });

  it("shows source attribution markers (HUMAN vs LLM)", () => {
    render(
      <ContextChain
        chain={mockChain}
        currentNodeId="PROJ-1.1.2"
        onNavigate={vi.fn()}
      />,
    );

    // First entry has [HUMAN-AUTHORED] marker.
    expect(screen.getAllByTestId("attribution-human")).toHaveLength(1);
    // Other entries are LLM-generated.
    expect(screen.getAllByTestId("attribution-llm")).toHaveLength(2);
  });

  it("is collapsible — shows summary by default", () => {
    render(
      <ContextChain
        chain={mockChain}
        currentNodeId="PROJ-1.1.2"
        onNavigate={vi.fn()}
      />,
    );

    // Full prompt text is NOT visible by default.
    expect(screen.queryByTestId("prompt-PROJ-1")).not.toBeInTheDocument();
  });

  it("expands to show full prompt text when clicked", () => {
    render(
      <ContextChain
        chain={mockChain}
        currentNodeId="PROJ-1.1.2"
        onNavigate={vi.fn()}
      />,
    );

    // Click the expand toggle.
    fireEvent.click(screen.getByLabelText("Toggle context chain"));

    // Full prompt text is now visible.
    expect(screen.getByTestId("prompt-PROJ-1")).toBeInTheDocument();
    expect(screen.getByTestId("prompt-PROJ-1.1")).toBeInTheDocument();
    expect(screen.getByTestId("prompt-PROJ-1.1.2")).toBeInTheDocument();
  });

  it("returns null for empty chain", () => {
    const { container } = render(
      <ContextChain
        chain={[]}
        currentNodeId="PROJ-1"
        onNavigate={vi.fn()}
      />,
    );

    expect(container.firstChild).toBeNull();
  });
});
