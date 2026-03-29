import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { PromptEditor } from "../PromptEditor";

/**
 * Prompt editor tests per MTIX-9.3.2.
 * Tests open/close, human addition block, save, save & rerun strategies,
 * cancel, annotations, and diff view.
 */

const defaultProps = {
  prompt: "Original prompt text",
  annotations: [],
  isOpen: true,
  onSave: vi.fn(),
  onSaveAndRerun: vi.fn(),
  onCancel: vi.fn(),
  onAddAnnotation: vi.fn(),
  onResolveAnnotation: vi.fn(),
};

describe("PromptEditor", () => {
  it("renders when isOpen is true", () => {
    render(<PromptEditor {...defaultProps} />);

    expect(screen.getByTestId("prompt-editor")).toBeInTheDocument();
    expect(screen.getByTestId("prompt-textarea")).toBeInTheDocument();
  });

  it("does not render when isOpen is false", () => {
    render(<PromptEditor {...defaultProps} isOpen={false} />);

    expect(screen.queryByTestId("prompt-editor")).not.toBeInTheDocument();
  });

  it("shows human addition block", () => {
    render(<PromptEditor {...defaultProps} />);

    expect(screen.getByTestId("human-addition-block")).toBeInTheDocument();
    expect(screen.getByLabelText("Human addition")).toBeInTheDocument();
  });

  it("calls onSave with prompt text", () => {
    const onSave = vi.fn();
    render(<PromptEditor {...defaultProps} onSave={onSave} />);

    // Modify the prompt.
    fireEvent.change(screen.getByTestId("prompt-textarea"), {
      target: { value: "Updated prompt" },
    });

    fireEvent.click(screen.getByTestId("save-button"));
    expect(onSave).toHaveBeenCalledWith("Updated prompt");
  });

  it("includes human addition in saved prompt", () => {
    const onSave = vi.fn();
    render(<PromptEditor {...defaultProps} onSave={onSave} />);

    fireEvent.change(screen.getByTestId("human-addition-textarea"), {
      target: { value: "Use jittered backoff" },
    });

    fireEvent.click(screen.getByTestId("save-button"));
    expect(onSave).toHaveBeenCalledWith(
      expect.stringContaining("[HUMAN-AUTHORED]"),
    );
    expect(onSave).toHaveBeenCalledWith(
      expect.stringContaining("Use jittered backoff"),
    );
  });

  it("shows rerun menu with 4 strategies", () => {
    render(<PromptEditor {...defaultProps} />);

    fireEvent.click(screen.getByTestId("save-rerun-button"));
    expect(screen.getByTestId("rerun-menu")).toBeInTheDocument();
    expect(screen.getByTestId("rerun-all")).toBeInTheDocument();
    expect(screen.getByTestId("rerun-open_only")).toBeInTheDocument();
    expect(screen.getByTestId("rerun-delete")).toBeInTheDocument();
    expect(screen.getByTestId("rerun-review")).toBeInTheDocument();
  });

  it("calls onSaveAndRerun with strategy", () => {
    const onSaveAndRerun = vi.fn();
    render(
      <PromptEditor {...defaultProps} onSaveAndRerun={onSaveAndRerun} />,
    );

    fireEvent.click(screen.getByTestId("save-rerun-button"));
    fireEvent.click(screen.getByTestId("rerun-all"));

    expect(onSaveAndRerun).toHaveBeenCalledWith(
      expect.any(String),
      "all",
    );
  });

  it("calls onCancel when cancel is clicked", () => {
    const onCancel = vi.fn();
    render(<PromptEditor {...defaultProps} onCancel={onCancel} />);

    fireEvent.click(screen.getByTestId("cancel-button"));
    expect(onCancel).toHaveBeenCalled();
  });

  it("displays existing annotations", () => {
    render(
      <PromptEditor
        {...defaultProps}
        annotations={[
          {
            id: "ann-1",
            author: "vimal",
            text: "Use jittered backoff",
            created_at: "2026-03-08T10:00:00Z",
            resolved: false,
          },
        ]}
      />,
    );

    expect(screen.getByText("Use jittered backoff")).toBeInTheDocument();
    expect(screen.getByText("vimal")).toBeInTheDocument();
  });

  it("allows adding new annotation", () => {
    const onAddAnnotation = vi.fn();
    render(
      <PromptEditor {...defaultProps} onAddAnnotation={onAddAnnotation} />,
    );

    // Open annotation input.
    fireEvent.click(screen.getByTestId("add-annotation-button"));
    expect(screen.getByTestId("annotation-input")).toBeInTheDocument();

    // Type and submit.
    fireEvent.change(screen.getByLabelText("Annotation text"), {
      target: { value: "New annotation" },
    });
    fireEvent.click(screen.getByTestId("submit-annotation"));

    expect(onAddAnnotation).toHaveBeenCalledWith("New annotation");
  });

  it("allows resolving annotations", () => {
    const onResolveAnnotation = vi.fn();
    render(
      <PromptEditor
        {...defaultProps}
        onResolveAnnotation={onResolveAnnotation}
        annotations={[
          {
            id: "ann-1",
            author: "vimal",
            text: "Check this",
            created_at: "2026-03-08T10:00:00Z",
            resolved: false,
          },
        ]}
      />,
    );

    fireEvent.click(screen.getByTestId("resolve-annotation-ann-1"));
    expect(onResolveAnnotation).toHaveBeenCalledWith("ann-1", true);
  });

  it("shows diff view when prompt changes", () => {
    render(<PromptEditor {...defaultProps} />);

    // Modify prompt to trigger diff.
    fireEvent.change(screen.getByTestId("prompt-textarea"), {
      target: { value: "New prompt text" },
    });

    // Toggle diff view.
    expect(screen.getByTestId("diff-toggle")).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("diff-toggle"));
    expect(screen.getByTestId("diff-view")).toBeInTheDocument();
  });
});
