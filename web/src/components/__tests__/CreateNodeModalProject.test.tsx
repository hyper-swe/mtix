import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ProjectProvider } from "../../contexts/ProjectContext";
import { CreateNodeModal } from "../CreateNodeModal";

/**
 * CreateNodeModal project-awareness tests per FR-MULTI-PROJECT (MP-17):
 * default-to-scope, child inheritance/lock, and the new-project confirm step.
 */

vi.mock("../../api", () => ({
  createNode: vi.fn().mockResolvedValue({ id: "MTIX-9", title: "T" }),
}));

vi.mock("../../api/nodes", () => ({
  getProjects: vi.fn().mockResolvedValue([
    { prefix: "MTIX", count: 42, isPrimary: true },
    { prefix: "MTIX-DEV-OPS", count: 7, isPrimary: false },
  ]),
}));

import * as api from "../../api";

function renderModal(props: Partial<React.ComponentProps<typeof CreateNodeModal>> = {}) {
  return render(
    <ProjectProvider>
      <CreateNodeModal
        isOpen
        onClose={props.onClose ?? vi.fn()}
        onCreated={props.onCreated ?? vi.fn()}
        defaultParentId={props.defaultParentId}
      />
    </ProjectProvider>,
  );
}

beforeEach(() => {
  window.localStorage.clear();
  // Scope the session to the primary so the create defaults to MTIX.
  window.localStorage.setItem("mtix-active-project", "MTIX");
  vi.mocked(api.createNode).mockClear();
});

describe("CreateNodeModal — project field", () => {
  it("defaults a root create to the active project scope", async () => {
    renderModal();

    // Combobox is present for root creates and prefilled with the scope.
    expect(screen.getByTestId("create-project")).toHaveValue("MTIX");

    fireEvent.change(screen.getByTestId("create-title"), {
      target: { value: "Root issue" },
    });
    fireEvent.click(screen.getByTestId("create-submit"));

    await waitFor(() => {
      expect(api.createNode).toHaveBeenCalledWith(
        expect.objectContaining({ title: "Root issue", project: "MTIX" }),
      );
    });
  });

  it("locks the project to the parent's for a child create", async () => {
    renderModal({ defaultParentId: "MTIX-DEV-OPS-1" });

    // No editable combobox for a child — a locked, inherited display instead.
    expect(screen.queryByTestId("create-project")).not.toBeInTheDocument();
    expect(screen.getByTestId("create-project-locked")).toHaveTextContent(
      "MTIX-DEV-OPS",
    );

    fireEvent.change(screen.getByTestId("create-title"), {
      target: { value: "Child issue" },
    });
    fireEvent.click(screen.getByTestId("create-submit"));

    await waitFor(() => {
      expect(api.createNode).toHaveBeenCalledWith(
        expect.objectContaining({
          title: "Child issue",
          parent_id: "MTIX-DEV-OPS-1",
          project: "MTIX-DEV-OPS",
        }),
      );
    });
  });

  it("requires confirmation before creating into a brand-new project", async () => {
    renderModal();

    // Wait for the project list to load so MTIX is recognized as existing.
    await waitFor(() => {
      expect(screen.getByTestId("create-project")).toBeInTheDocument();
    });

    fireEvent.change(screen.getByTestId("create-title"), {
      target: { value: "Bootstrap" },
    });
    fireEvent.change(screen.getByTestId("create-project"), {
      target: { value: "NEWPROJ" },
    });
    fireEvent.click(screen.getByTestId("create-submit"));

    // Confirm step appears; nothing created yet.
    expect(screen.getByTestId("new-project-confirm")).toBeInTheDocument();
    expect(api.createNode).not.toHaveBeenCalled();

    fireEvent.click(screen.getByTestId("new-project-confirm-button"));

    await waitFor(() => {
      expect(api.createNode).toHaveBeenCalledWith(
        expect.objectContaining({ title: "Bootstrap", project: "NEWPROJ" }),
      );
    });
  });

  it("rejects an invalid project prefix without confirming", async () => {
    renderModal();

    fireEvent.change(screen.getByTestId("create-title"), {
      target: { value: "Oops" },
    });
    fireEvent.change(screen.getByTestId("create-project"), {
      target: { value: "bad prefix" },
    });
    fireEvent.click(screen.getByTestId("create-submit"));

    expect(screen.getByTestId("create-error")).toHaveTextContent("Invalid project");
    expect(screen.queryByTestId("new-project-confirm")).not.toBeInTheDocument();
    expect(api.createNode).not.toHaveBeenCalled();
  });
});
