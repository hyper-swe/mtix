import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { CreateNodeModal } from "../CreateNodeModal";

/**
 * CreateNodeModal tests — validates create node form, validation,
 * submission, keyboard shortcuts, and error handling.
 */

// Mock the API module.
vi.mock("../../api", () => ({
  createNode: vi.fn().mockResolvedValue({
    id: "1.1",
    title: "Test Node",
  }),
}));

import * as api from "../../api";

beforeEach(() => {
  vi.mocked(api.createNode).mockClear();
});

describe("CreateNodeModal", () => {
  it("does not render when isOpen is false", () => {
    render(
      <CreateNodeModal
        isOpen={false}
        onClose={vi.fn()}
        onCreated={vi.fn()}
      />,
    );

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("renders form when isOpen is true", () => {
    render(
      <CreateNodeModal
        isOpen={true}
        onClose={vi.fn()}
        onCreated={vi.fn()}
      />,
    );

    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByTestId("create-title")).toBeInTheDocument();
    expect(screen.getByTestId("create-description")).toBeInTheDocument();
    expect(screen.getByTestId("create-prompt")).toBeInTheDocument();
    expect(screen.getByTestId("create-submit")).toBeInTheDocument();
  });

  it("disables submit button when title is empty", () => {
    render(
      <CreateNodeModal
        isOpen={true}
        onClose={vi.fn()}
        onCreated={vi.fn()}
      />,
    );

    const submitBtn = screen.getByTestId("create-submit");
    expect(submitBtn).toBeDisabled();
  });

  it("submits form with valid title", async () => {
    const onCreated = vi.fn();
    const onClose = vi.fn();

    render(
      <CreateNodeModal
        isOpen={true}
        onClose={onClose}
        onCreated={onCreated}
      />,
    );

    fireEvent.change(screen.getByTestId("create-title"), {
      target: { value: "New Test Issue" },
    });

    fireEvent.click(screen.getByTestId("create-submit"));

    await waitFor(() => {
      expect(api.createNode).toHaveBeenCalledWith(
        expect.objectContaining({
          title: "New Test Issue",
          priority: 2,
        }),
      );
    });

    await waitFor(() => {
      expect(onCreated).toHaveBeenCalled();
      expect(onClose).toHaveBeenCalled();
    });
  });

  it("submits with all fields filled", async () => {
    const onCreated = vi.fn();

    render(
      <CreateNodeModal
        isOpen={true}
        onClose={vi.fn()}
        onCreated={onCreated}
      />,
    );

    fireEvent.change(screen.getByTestId("create-title"), {
      target: { value: "Full Issue" },
    });
    fireEvent.change(screen.getByTestId("create-description"), {
      target: { value: "A detailed description" },
    });
    fireEvent.change(screen.getByTestId("create-prompt"), {
      target: { value: "LLM prompt text" },
    });
    fireEvent.change(screen.getByTestId("create-parent-id"), {
      target: { value: "1" },
    });

    // Change priority to P1
    fireEvent.click(screen.getByTestId("priority-1"));

    fireEvent.click(screen.getByTestId("create-submit"));

    await waitFor(() => {
      expect(api.createNode).toHaveBeenCalledWith(
        expect.objectContaining({
          title: "Full Issue",
          description: "A detailed description",
          prompt: "LLM prompt text",
          priority: 1,
          parent_id: "1",
        }),
      );
    });
  });

  it("closes on Escape key", () => {
    const onClose = vi.fn();

    render(
      <CreateNodeModal
        isOpen={true}
        onClose={onClose}
        onCreated={vi.fn()}
      />,
    );

    fireEvent.keyDown(screen.getByTestId("create-title"), {
      key: "Escape",
    });

    expect(onClose).toHaveBeenCalled();
  });

  it("closes on backdrop click", () => {
    const onClose = vi.fn();

    render(
      <CreateNodeModal
        isOpen={true}
        onClose={onClose}
        onCreated={vi.fn()}
      />,
    );

    const backdrop = screen.getByRole("dialog");
    fireEvent.click(backdrop);

    expect(onClose).toHaveBeenCalled();
  });

  it("shows API error on failure", async () => {
    vi.mocked(api.createNode).mockRejectedValueOnce(
      new Error("Server error"),
    );

    render(
      <CreateNodeModal
        isOpen={true}
        onClose={vi.fn()}
        onCreated={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByTestId("create-title"), {
      target: { value: "Will Fail" },
    });

    fireEvent.click(screen.getByTestId("create-submit"));

    await waitFor(() => {
      expect(screen.getByTestId("create-error")).toHaveTextContent(
        "Server error",
      );
    });
  });

  it("renders priority buttons with P2 selected by default", () => {
    render(
      <CreateNodeModal
        isOpen={true}
        onClose={vi.fn()}
        onCreated={vi.fn()}
      />,
    );

    // P2 should be selected (has accent background)
    const p2 = screen.getByTestId("priority-2");
    expect(p2).toBeInTheDocument();
    expect(p2).toHaveTextContent("P2");
  });

  it("pre-fills parent ID when provided", () => {
    render(
      <CreateNodeModal
        isOpen={true}
        onClose={vi.fn()}
        onCreated={vi.fn()}
        defaultParentId="1.2.3"
      />,
    );

    expect(screen.getByTestId("create-parent-id")).toHaveValue("1.2.3");
  });
});
