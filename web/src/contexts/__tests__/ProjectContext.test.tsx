import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ProjectProvider, useProject, ALL_PROJECTS } from "../ProjectContext";

/**
 * ProjectContext tests per FR-MULTI-PROJECT (MP-14): active scope state,
 * primary-default seeding from GET /projects, localStorage persistence, and
 * derived flags.
 */

// Mock the projects endpoint that the provider loads on mount.
vi.mock("../../api/nodes", () => ({
  getProjects: vi.fn(),
}));

import { getProjects } from "../../api/nodes";

const MULTI = [
  { prefix: "MTIX", count: 42, isPrimary: true },
  { prefix: "MTIX-DEV-OPS", count: 7, isPrimary: false },
];

function Consumer() {
  const {
    activeScope,
    primary,
    isAll,
    isMultiProject,
    projects,
    defaultCreateProject,
    setActiveScope,
  } = useProject();
  return (
    <div>
      <span data-testid="scope">{activeScope}</span>
      <span data-testid="primary">{primary ?? "none"}</span>
      <span data-testid="is-all">{String(isAll)}</span>
      <span data-testid="is-multi">{String(isMultiProject)}</span>
      <span data-testid="count">{projects.length}</span>
      <span data-testid="default-create">{defaultCreateProject ?? "none"}</span>
      <button onClick={() => setActiveScope("MTIX-DEV-OPS")}>scope-ops</button>
      <button onClick={() => setActiveScope(ALL_PROJECTS)}>scope-all</button>
    </div>
  );
}

beforeEach(() => {
  window.localStorage.clear();
  vi.mocked(getProjects).mockReset();
});

describe("ProjectContext", () => {
  it("defaults the active scope to the primary project after load", async () => {
    vi.mocked(getProjects).mockResolvedValue(MULTI);

    render(
      <ProjectProvider>
        <Consumer />
      </ProjectProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("scope")).toHaveTextContent("MTIX");
    });
    expect(screen.getByTestId("primary")).toHaveTextContent("MTIX");
    expect(screen.getByTestId("is-multi")).toHaveTextContent("true");
    expect(screen.getByTestId("count")).toHaveTextContent("2");
  });

  it("honors a valid persisted scope over the primary default", async () => {
    window.localStorage.setItem("mtix-active-project", "MTIX-DEV-OPS");
    vi.mocked(getProjects).mockResolvedValue(MULTI);

    render(
      <ProjectProvider>
        <Consumer />
      </ProjectProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("scope")).toHaveTextContent("MTIX-DEV-OPS");
    });
  });

  it("falls back to the primary when the persisted scope no longer resolves", async () => {
    window.localStorage.setItem("mtix-active-project", "GONE");
    vi.mocked(getProjects).mockResolvedValue(MULTI);

    render(
      <ProjectProvider>
        <Consumer />
      </ProjectProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("scope")).toHaveTextContent("MTIX");
    });
  });

  it("persists scope changes to localStorage and exposes isAll", async () => {
    vi.mocked(getProjects).mockResolvedValue(MULTI);

    render(
      <ProjectProvider>
        <Consumer />
      </ProjectProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("scope")).toHaveTextContent("MTIX");
    });

    fireEvent.click(screen.getByText("scope-all"));
    expect(screen.getByTestId("scope")).toHaveTextContent(ALL_PROJECTS);
    expect(screen.getByTestId("is-all")).toHaveTextContent("true");
    expect(window.localStorage.getItem("mtix-active-project")).toBe("all");
    // In all-projects mode a new root defaults into the primary.
    expect(screen.getByTestId("default-create")).toHaveTextContent("MTIX");
  });

  it("treats a single-project DB as non-multi", async () => {
    vi.mocked(getProjects).mockResolvedValue([
      { prefix: "MTIX", count: 5, isPrimary: true },
    ]);

    render(
      <ProjectProvider>
        <Consumer />
      </ProjectProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("scope")).toHaveTextContent("MTIX");
    });
    expect(screen.getByTestId("is-multi")).toHaveTextContent("false");
  });

  it("throws when useProject is used outside a provider", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    expect(() => render(<Consumer />)).toThrow(
      "useProject must be used within a ProjectProvider",
    );
    spy.mockRestore();
  });
});
