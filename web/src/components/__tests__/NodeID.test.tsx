import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { ProjectProvider } from "../../contexts/ProjectContext";
import { NodeID } from "../NodeID";

/**
 * NodeID tests per FR-MULTI-PROJECT (MP-18, D4): a project-prefix badge in the
 * all-projects scope only, plain id otherwise, and graceful degradation when
 * rendered outside a ProjectProvider.
 */

vi.mock("../../api/nodes", () => ({
  getProjects: vi.fn(),
}));

import { getProjects } from "../../api/nodes";

const MULTI = [
  { prefix: "MTIX", count: 42, isPrimary: true },
  { prefix: "MTIX-DEV-OPS", count: 7, isPrimary: false },
];

beforeEach(() => {
  window.localStorage.clear();
  vi.mocked(getProjects).mockReset();
});

describe("NodeID", () => {
  it("renders the plain id with no badge outside a ProjectProvider", () => {
    render(<NodeID id="MTIX-1.2" testId="nid" />);
    expect(screen.getByTestId("nid")).toHaveTextContent("MTIX-1.2");
    expect(screen.queryByTestId("node-id-badge")).not.toBeInTheDocument();
  });

  it("shows a project-prefix badge in the all-projects scope", async () => {
    window.localStorage.setItem("mtix-active-project", "all");
    vi.mocked(getProjects).mockResolvedValue(MULTI);

    render(
      <ProjectProvider>
        <NodeID id="MTIX-DEV-OPS-3.1" testId="nid" />
      </ProjectProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("node-id-badge")).toHaveTextContent("MTIX-DEV-OPS");
    });
    expect(screen.getByTestId("nid")).toHaveTextContent("MTIX-DEV-OPS-3.1");
  });

  it("renders no badge in a single/scoped project view", async () => {
    window.localStorage.setItem("mtix-active-project", "MTIX");
    vi.mocked(getProjects).mockResolvedValue(MULTI);

    render(
      <ProjectProvider>
        <NodeID id="MTIX-1" testId="nid" />
      </ProjectProvider>,
    );

    // Allow the provider to resolve, then assert the badge stays absent.
    await waitFor(() => {
      expect(screen.getByTestId("nid")).toBeInTheDocument();
    });
    expect(screen.queryByTestId("node-id-badge")).not.toBeInTheDocument();
  });

  it("renders the short trailing form when short is set", () => {
    render(<NodeID id="MTIX-1.2.3" short testId="nid" />);
    expect(screen.getByTestId("nid")).toHaveTextContent(".3");
  });
});
