import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { StatusBadge } from "../StatusBadge";

/**
 * Status badge tests per MTIX-9.3.1.
 * Tests status transitions, forward vs destructive, and confirmation popover.
 */

describe("StatusBadge", () => {
  it("renders current status", () => {
    render(
      <StatusBadge status="in_progress" onStatusChange={vi.fn()} />,
    );

    expect(screen.getByTestId("status-badge")).toHaveTextContent(
      "IN PROGRESS",
    );
  });

  it("shows valid transitions on hover", () => {
    render(
      <StatusBadge status="in_progress" onStatusChange={vi.fn()} />,
    );

    fireEvent.mouseEnter(screen.getByTestId("status-badge"));
    expect(screen.getByTestId("transition-popover")).toBeInTheDocument();
    expect(screen.getByTestId("transition-done")).toBeInTheDocument();
    expect(screen.getByTestId("transition-deferred")).toBeInTheDocument();
    expect(screen.getByTestId("transition-cancelled")).toBeInTheDocument();
  });

  it("single click forward transition works", () => {
    const onStatusChange = vi.fn();
    render(
      <StatusBadge
        status="in_progress"
        onStatusChange={onStatusChange}
      />,
    );

    fireEvent.mouseEnter(screen.getByTestId("status-badge"));
    fireEvent.click(screen.getByTestId("transition-done"));

    expect(onStatusChange).toHaveBeenCalledWith("done");
  });

  it("destructive transition shows confirmation popover", () => {
    render(
      <StatusBadge status="in_progress" onStatusChange={vi.fn()} />,
    );

    fireEvent.mouseEnter(screen.getByTestId("status-badge"));
    fireEvent.click(screen.getByTestId("transition-cancelled"));

    // Should show confirm popover, not transition directly.
    expect(screen.getByTestId("confirm-popover")).toBeInTheDocument();
    expect(screen.getByText(/Confirm CANCELLED/)).toBeInTheDocument();
  });

  it("confirms destructive transition", () => {
    const onStatusChange = vi.fn();
    render(
      <StatusBadge
        status="in_progress"
        onStatusChange={onStatusChange}
      />,
    );

    fireEvent.mouseEnter(screen.getByTestId("status-badge"));
    fireEvent.click(screen.getByTestId("transition-cancelled"));
    fireEvent.click(screen.getByTestId("confirm-destructive"));

    expect(onStatusChange).toHaveBeenCalledWith("cancelled");
  });

  it("cancels destructive transition", () => {
    const onStatusChange = vi.fn();
    render(
      <StatusBadge
        status="in_progress"
        onStatusChange={onStatusChange}
      />,
    );

    fireEvent.mouseEnter(screen.getByTestId("status-badge"));
    fireEvent.click(screen.getByTestId("transition-cancelled"));
    fireEvent.click(screen.getByTestId("cancel-destructive"));

    expect(onStatusChange).not.toHaveBeenCalled();
  });

  it("shows correct transitions for open status", () => {
    render(
      <StatusBadge status="open" onStatusChange={vi.fn()} />,
    );

    fireEvent.mouseEnter(screen.getByTestId("status-badge"));
    expect(screen.getByTestId("transition-in_progress")).toBeInTheDocument();
    expect(screen.getByTestId("transition-deferred")).toBeInTheDocument();
    expect(screen.getByTestId("transition-cancelled")).toBeInTheDocument();
  });
});
