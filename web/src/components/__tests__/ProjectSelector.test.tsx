import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ProjectProvider } from "../../contexts/ProjectContext";
import { ProjectSelector } from "../ProjectSelector";

/**
 * ProjectSelector tests per FR-MULTI-PROJECT (MP-15): a scope dropdown when
 * multiple projects exist, and an unobtrusive single-name display otherwise.
 */

vi.mock("../../api/nodes", () => ({
  getProjects: vi.fn(),
}));

import { getProjects } from "../../api/nodes";

beforeEach(() => {
  window.localStorage.clear();
  vi.mocked(getProjects).mockReset();
});

function renderSelector() {
  return render(
    <ProjectProvider>
      <ProjectSelector />
    </ProjectProvider>,
  );
}

describe("ProjectSelector", () => {
  it("renders an unobtrusive single name when only one project exists", async () => {
    vi.mocked(getProjects).mockResolvedValue([
      { prefix: "MTIX", count: 9, isPrimary: true },
    ]);

    renderSelector();

    await waitFor(() => {
      expect(screen.getByTestId("project-selector-single")).toHaveTextContent("MTIX");
    });
    // No dropdown affordance in single-project mode.
    expect(screen.queryByTestId("project-selector-button")).not.toBeInTheDocument();
  });

  it("renders a dropdown of projects plus All projects when multi-project", async () => {
    vi.mocked(getProjects).mockResolvedValue([
      { prefix: "MTIX", count: 42, isPrimary: true },
      { prefix: "MTIX-DEV-OPS", count: 7, isPrimary: false },
    ]);

    renderSelector();

    // Button shows the primary scope label by default.
    await waitFor(() => {
      expect(screen.getByTestId("project-selector-button")).toHaveTextContent("MTIX");
    });

    fireEvent.click(screen.getByTestId("project-selector-button"));

    expect(screen.getByTestId("project-option-MTIX")).toBeInTheDocument();
    expect(screen.getByTestId("project-option-MTIX-DEV-OPS")).toBeInTheDocument();
    expect(screen.getByTestId("project-option-all")).toHaveTextContent("All projects");
  });

  it("switches the active scope when an option is chosen", async () => {
    vi.mocked(getProjects).mockResolvedValue([
      { prefix: "MTIX", count: 42, isPrimary: true },
      { prefix: "MTIX-DEV-OPS", count: 7, isPrimary: false },
    ]);

    renderSelector();

    await waitFor(() => {
      expect(screen.getByTestId("project-selector-button")).toBeInTheDocument();
    });

    fireEvent.click(screen.getByTestId("project-selector-button"));
    fireEvent.click(screen.getByTestId("project-option-MTIX-DEV-OPS"));

    expect(screen.getByTestId("project-selector-button")).toHaveTextContent(
      "MTIX-DEV-OPS",
    );
    expect(window.localStorage.getItem("mtix-active-project")).toBe("MTIX-DEV-OPS");
  });
});
