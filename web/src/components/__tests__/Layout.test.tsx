import { describe, it, expect, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { App } from "../../App";

/**
 * Layout tests per MTIX-9.1.2.
 * Tests two-panel layout, sidebar collapse, resize, breadcrumb, and mobile.
 */

beforeEach(() => {
  window.localStorage.clear();
  document.documentElement.classList.remove("dark", "density-compact");
});

describe("Layout", () => {
  it("renders two panels — sidebar and main content", () => {
    render(<App />);

    // Sidebar views should be present.
    expect(screen.getByText("Dashboard")).toBeInTheDocument();
    expect(screen.getByText("Stale Board")).toBeInTheDocument();
  });

  it("collapses sidebar on toggle button click", () => {
    render(<App />);

    const toggleBtn = screen.getByLabelText("Collapse sidebar");
    // Dashboard button exists in sidebar.
    expect(screen.getByText("Dashboard")).toBeInTheDocument();

    fireEvent.click(toggleBtn);

    // After collapse, sidebar-specific content should be hidden.
    expect(screen.queryByText("Dashboard")).not.toBeInTheDocument();
  });

  it("renders breadcrumb bar at bottom", () => {
    render(<App />);
    // Breadcrumb shows connection status (may appear in both TopBar and Breadcrumb).
    expect(screen.getAllByText("Disconnected").length).toBeGreaterThan(0);
  });

  it("renders top bar with project selector", () => {
    render(<App />);
    expect(screen.getByText("Select Project")).toBeInTheDocument();
  });

  it("renders top bar with search trigger", () => {
    render(<App />);
    expect(screen.getByLabelText("Search (Cmd+K)")).toBeInTheDocument();
  });

  it("shows connection status in breadcrumb", () => {
    render(<App />);
    // WebSocket won't connect in test env, so it should show disconnected state.
    // Status text may appear in both TopBar and Breadcrumb.
    const matches = screen.getAllByText(/Disconnected|Connected|Reconnecting/);
    expect(matches.length).toBeGreaterThan(0);
  });

  it("persists sidebar collapsed state to localStorage", () => {
    render(<App />);

    const toggleBtn = screen.getByLabelText("Collapse sidebar");
    fireEvent.click(toggleBtn);

    expect(window.localStorage.getItem("mtix-sidebar-collapsed")).toBe("true");
  });
});

describe("Sidebar resize", () => {
  it("renders resize handle when sidebar is visible", () => {
    render(<App />);
    const handle = screen.getByRole("separator", {
      name: "Resize sidebar",
    });
    expect(handle).toBeInTheDocument();
  });
});
