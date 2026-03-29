import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { ProgressBar } from "../ProgressBar";

/**
 * Progress bar tests per MTIX-9.3.5.
 * Tests color ranges, zero/100 percent, label, and animation.
 */

describe("ProgressBar", () => {
  it("renders with correct percentage", () => {
    render(<ProgressBar progress={0.66} showLabel />);

    const bar = screen.getByRole("progressbar");
    expect(bar).toHaveAttribute("aria-valuenow", "66");
    expect(bar).toHaveAttribute("aria-label", "66% complete");
    expect(screen.getByText("66%")).toBeInTheDocument();
  });

  it("uses red color for 0-25%", () => {
    render(<ProgressBar progress={0.1} />);

    const fill = screen.getByTestId("progress-fill");
    expect(fill.style.backgroundColor).toBe("var(--color-status-blocked)");
  });

  it("uses blue color for 25-75%", () => {
    render(<ProgressBar progress={0.5} />);

    const fill = screen.getByTestId("progress-fill");
    expect(fill.style.backgroundColor).toBe("var(--color-status-in-progress)");
  });

  it("uses green color for 75-100%", () => {
    render(<ProgressBar progress={0.9} />);

    const fill = screen.getByTestId("progress-fill");
    expect(fill.style.backgroundColor).toBe("var(--color-status-done)");
  });

  it("renders zero percent correctly", () => {
    render(<ProgressBar progress={0} showLabel />);

    const bar = screen.getByRole("progressbar");
    expect(bar).toHaveAttribute("aria-valuenow", "0");
    expect(screen.getByText("0%")).toBeInTheDocument();
    const fill = screen.getByTestId("progress-fill");
    expect(fill.style.width).toBe("0%");
  });

  it("renders 100 percent correctly", () => {
    render(<ProgressBar progress={1} showLabel />);

    const bar = screen.getByRole("progressbar");
    expect(bar).toHaveAttribute("aria-valuenow", "100");
    expect(screen.getByText("100%")).toBeInTheDocument();
    const fill = screen.getByTestId("progress-fill");
    expect(fill.style.width).toBe("100%");
  });

  it("has smooth transition for animation", () => {
    render(<ProgressBar progress={0.5} />);

    const fill = screen.getByTestId("progress-fill");
    expect(fill.style.transition).toContain("300ms");
  });

  it("clamps progress to 0-1 range", () => {
    render(<ProgressBar progress={1.5} showLabel />);

    const bar = screen.getByRole("progressbar");
    expect(bar).toHaveAttribute("aria-valuenow", "100");
  });
});
