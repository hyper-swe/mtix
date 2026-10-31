import { describe, it, expect, beforeEach } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { NavigationProvider, useNavigation } from "../NavigationContext";

function Probe() {
  const nav = useNavigation();
  return (
    <div>
      <span data-testid="view">{nav.view}</span>
      <span data-testid="sel">{nav.selectedNodeId ?? "none"}</span>
      <span data-testid="invalid">{String(nav.invalidTicketRef)}</span>
      <button onClick={() => nav.selectNode("MTIX-7.1")}>select</button>
      <button onClick={() => nav.goBack()}>back</button>
    </div>
  );
}

function mount(url: string) {
  window.history.replaceState(null, "", url);
  return render(
    <NavigationProvider>
      <Probe />
    </NavigationProvider>,
  );
}

describe("NavigationProvider deep links (MTIX-230)", () => {
  beforeEach(() => window.history.replaceState(null, "", "/"));

  it("selects the ticket named by /ticket/<id> on first load", () => {
    mount("/ticket/MTIX-230.2");
    expect(screen.getByTestId("view").textContent).toBe("node-detail");
    expect(screen.getByTestId("sel").textContent).toBe("MTIX-230.2");
  });

  it("rewrites ?node=<id> to the canonical path and keeps other filters", () => {
    mount("/?status=open&node=MTIX-5");
    expect(window.location.pathname).toBe("/ticket/MTIX-5");
    expect(window.location.search).toBe("?status=open");
    expect(screen.getByTestId("sel").textContent).toBe("MTIX-5");
  });

  it("pushes the ticket path on select and keeps the filter query", () => {
    mount("/?status=open");
    const before = window.history.length;
    act(() => screen.getByText("select").click());
    expect(window.location.pathname).toBe("/ticket/MTIX-7.1");
    expect(window.location.search).toBe("?status=open");
    expect(window.history.length).toBe(before + 1);
  });

  it("returns to / with filters kept on deselect", () => {
    mount("/ticket/MTIX-1?status=open");
    act(() => screen.getByText("back").click());
    expect(window.location.pathname).toBe("/");
    expect(window.location.search).toBe("?status=open");
    expect(screen.getByTestId("sel").textContent).toBe("none");
  });

  it("re-selects and deselects on popstate without reload", () => {
    mount("/");
    act(() => {
      window.history.pushState(null, "", "/ticket/MTIX-9");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(screen.getByTestId("sel").textContent).toBe("MTIX-9");
    act(() => {
      window.history.pushState(null, "", "/");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(screen.getByTestId("sel").textContent).toBe("none");
    expect(screen.getByTestId("view").textContent).toBe("all-issues");
  });

  it.each([
    "/ticket/<script>alert(1)</script>",
    "/ticket/..%2f..%2fetc%2fpasswd",
    "/ticket/MTIX-1/extra",
  ])("never selects a hostile id %s and keeps the URL", (p) => {
    mount(p);
    expect(screen.getByTestId("sel").textContent).toBe("none");
    expect(screen.getByTestId("view").textContent).toBe("node-detail");
    expect(screen.getByTestId("invalid").textContent).toBe("true");
    expect(window.location.pathname + window.location.search).toBe(new URL(p, "http://localhost").pathname);
  });
});
