import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { ProgressRing } from "../ProgressRing";

/**
 * Progress ring tests per MTIX-9.3.5.
 */

describe("ProgressRing", () => {
  it("renders with correct percentage", () => {
    render(<ProgressRing progress={0.5} />);

    const ring = screen.getByRole("progressbar");
    expect(ring).toHaveAttribute("aria-valuenow", "50");
  });

  it("renders zero percent", () => {
    render(<ProgressRing progress={0} />);

    const ring = screen.getByRole("progressbar");
    expect(ring).toHaveAttribute("aria-valuenow", "0");
  });

  it("renders 100 percent", () => {
    render(<ProgressRing progress={1} />);

    const ring = screen.getByRole("progressbar");
    expect(ring).toHaveAttribute("aria-valuenow", "100");
  });

  it("respects custom size", () => {
    render(<ProgressRing progress={0.5} size={32} />);

    const ring = screen.getByRole("progressbar");
    expect(ring).toHaveAttribute("width", "32");
    expect(ring).toHaveAttribute("height", "32");
  });

  it("has smooth transition on progress arc", () => {
    render(<ProgressRing progress={0.5} />);

    const ring = screen.getByRole("progressbar");
    const progressCircle = ring.querySelectorAll("circle")[1] as SVGElement;
    expect(progressCircle.style.transition).toContain("300ms");
  });
});
