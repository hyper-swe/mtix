import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { ContextChain, levelIndicator } from "../ContextChain";
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

/**
 * MTIX-13: post-v0.1.1-beta canonical depth-to-letter mapping.
 *
 * After the v0.1.1-beta hierarchy swap (depth 0 = epic, depth 1 = story),
 * the level indicator must follow the canonical mapping that matches
 * Go's NodeTypeForDepth:
 *   depth 0 -> "E" (Epic)
 *   depth 1 -> "S" (Story)
 *   depth 2 -> "I" (Issue)
 *   depth 3+ -> "M" (Micro)
 *
 * Pre-fix, the function returned "S" at depth 0 and "E" at depth 1
 * (the old convention) and "I" for any depth >= 2 (missing Micro).
 */
describe("levelIndicator (MTIX-13 canonical mapping)", () => {
  it("returns E for depth 0 (Epic)", () => {
    expect(levelIndicator(0)).toBe("E");
  });

  it("returns S for depth 1 (Story)", () => {
    expect(levelIndicator(1)).toBe("S");
  });

  it("returns I for depth 2 (Issue)", () => {
    expect(levelIndicator(2)).toBe("I");
  });

  it("returns M for depth 3 (Micro)", () => {
    expect(levelIndicator(3)).toBe("M");
  });

  it("returns M for deep nodes (depth 7)", () => {
    expect(levelIndicator(7)).toBe("M");
  });
});

describe("ContextChain rendered badges (MTIX-13 strict per-depth)", () => {
  // Strict per-depth assertion: the existing "shows level indicators (S/E/I)"
  // test only checked that some badge of each letter exists — it would pass
  // even with the inversion bug. These tests bind the badge letter to the
  // specific node's chain entry so a regression of the swap is caught.
  it("depth-0 entry renders 'E' badge (canonical Epic letter)", () => {
    render(
      <ContextChain
        chain={mockChain}
        currentNodeId="PROJ-1.1.2"
        onNavigate={vi.fn()}
      />,
    );
    const root = screen.getByTestId("chain-entry-PROJ-1");
    expect(within(root).getByTestId("level-E")).toBeInTheDocument();
  });

  it("depth-1 entry renders 'S' badge (canonical Story letter)", () => {
    render(
      <ContextChain
        chain={mockChain}
        currentNodeId="PROJ-1.1.2"
        onNavigate={vi.fn()}
      />,
    );
    const child = screen.getByTestId("chain-entry-PROJ-1.1");
    expect(within(child).getByTestId("level-S")).toBeInTheDocument();
  });

  it("depth-2 entry renders 'I' badge (Issue)", () => {
    render(
      <ContextChain
        chain={mockChain}
        currentNodeId="PROJ-1.1.2"
        onNavigate={vi.fn()}
      />,
    );
    const leaf = screen.getByTestId("chain-entry-PROJ-1.1.2");
    expect(within(leaf).getByTestId("level-I")).toBeInTheDocument();
  });
});
