import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { BreadcrumbProgress } from "../BreadcrumbProgress";

/**
 * BreadcrumbProgress tests per MTIX-9.3.5.
 */

describe("BreadcrumbProgress", () => {
  it("renders overall stats", () => {
    render(
      <BreadcrumbProgress
        progress={0.66}
        openCount={3}
        activeAgents={1}
      />,
    );

    const container = screen.getByTestId("breadcrumb-progress");
    expect(container).toBeInTheDocument();
    expect(screen.getByText("66% overall")).toBeInTheDocument();
    expect(screen.getByText("3 open")).toBeInTheDocument();
    expect(screen.getByText("1 agent active")).toBeInTheDocument();
  });

  it("pluralizes agents correctly", () => {
    render(
      <BreadcrumbProgress
        progress={0.5}
        openCount={0}
        activeAgents={3}
      />,
    );

    expect(screen.getByText("3 agents active")).toBeInTheDocument();
  });

  it("shows 0% for zero progress", () => {
    render(
      <BreadcrumbProgress
        progress={0}
        openCount={10}
        activeAgents={0}
      />,
    );

    expect(screen.getByText("0% overall")).toBeInTheDocument();
  });
});
