import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
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
      <button onClick={() => nav.navigateTo("dashboard")}>dashboard</button>
      <button onClick={() => nav.navigateTo("stale")}>stale</button>
      <button onClick={() => nav.navigateTo("agents")}>agents</button>
      <button onClick={() => nav.selectNode("")}>select-empty</button>
      <button onClick={() => nav.selectNode("MTIX-7.1")}>select-again</button>
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
  afterEach(() => vi.restoreAllMocks());

  it("selects the ticket named by /ticket/<id> on first load", () => {
    mount("/ticket/MTIX-230.2");
    expect(screen.getByTestId("view").textContent).toBe("node-detail");
    expect(screen.getByTestId("sel").textContent).toBe("MTIX-230.2");
  });

  it("rewrites ?node=<id> to the canonical path and keeps other filters", () => {
    window.history.replaceState(null, "", "/?status=open&node=MTIX-5");
    const before = window.history.length;
    mount("/?status=open&node=MTIX-5");
    expect(window.history.length).toBe(before);
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
    expect(screen.getByTestId("view").textContent).toBe("node-detail");
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

  it("navigateTo another view leaves /ticket/<id> and keeps the filter query", () => {
    mount("/ticket/MTIX-1?status=open");
    act(() => screen.getByText("dashboard").click());
    expect(window.location.pathname).toBe("/");
    expect(window.location.search).toBe("?status=open");
    expect(screen.getByTestId("view").textContent).toBe("dashboard");
    expect(screen.getByTestId("sel").textContent).toBe("none");
  });

  it("selecting a ticket after an invalid deep link clears the not-found state", () => {
    mount("/ticket/%3Cscript%3E");
    expect(screen.getByTestId("invalid").textContent).toBe("true");
    act(() => screen.getByText("select").click());
    expect(screen.getByTestId("invalid").textContent).toBe("false");
    expect(screen.getByTestId("sel").textContent).toBe("MTIX-7.1");
  });

  it("popstate from an invalid deep link to a valid ticket clears the not-found state", () => {
    mount("/ticket/%3Cscript%3E");
    act(() => {
      window.history.pushState(null, "", "/ticket/MTIX-2");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(screen.getByTestId("invalid").textContent).toBe("false");
    expect(screen.getByTestId("sel").textContent).toBe("MTIX-2");
  });

  it("goBack from the invalid state leaves the not-found state", () => {
    mount("/ticket/%3Cscript%3E");
    act(() => screen.getByText("back").click());
    expect(screen.getByTestId("invalid").textContent).toBe("false");
    expect(window.location.pathname).toBe("/");
  });

  it("selecting an empty id (the Go back button) deselects to / and never pushes /ticket/", () => {
    mount("/ticket/MTIX-1?status=open");
    act(() => screen.getByText("select-empty").click());
    expect(window.location.pathname).toBe("/");
    expect(window.location.search).toBe("?status=open");
    expect(screen.getByTestId("sel").textContent).toBe("none");
    expect(screen.getByTestId("view").textContent).toBe("all-issues");
  });

  it("reselecting the same ticket adds no duplicate history entry, with a filter query present", () => {
    mount("/?status=open");
    act(() => screen.getByText("select").click());
    const after = window.history.length;
    act(() => screen.getByText("select-again").click());
    expect(window.history.length).toBe(after);
    expect(window.location.pathname).toBe("/ticket/MTIX-7.1");
    expect(window.location.search).toBe("?status=open");
  });

  it.each(["dashboard", "stale", "agents"])(
    "selecting a ticket from the %s view opens the node detail view",
    (viewButton) => {
      mount("/");
      act(() => screen.getByText(viewButton).click());
      expect(screen.getByTestId("view").textContent).toBe(viewButton);
      act(() => screen.getByText("select").click());
      expect(screen.getByTestId("view").textContent).toBe("node-detail");
      expect(screen.getByTestId("sel").textContent).toBe("MTIX-7.1");
    },
  );

  it("goBack from an invalid ?node alias removes the alias and keeps the other query", () => {
    mount("/?status=open&node=%3Cx%3E");
    expect(screen.getByTestId("invalid").textContent).toBe("true");
    act(() => screen.getByText("back").click());
    expect(window.location.pathname).toBe("/");
    expect(window.location.search).toBe("?status=open");
    expect(screen.getByTestId("invalid").textContent).toBe("false");
  });

  it("navigateTo from / adds no history entry", () => {
    mount("/?status=open");
    const before = window.history.length;
    act(() => screen.getByText("dashboard").click());
    expect(window.history.length).toBe(before);
    expect(window.location.pathname + window.location.search).toBe("/?status=open");
  });

  it("removes its popstate listener on unmount", () => {
    const add = vi.spyOn(window, "addEventListener");
    const remove = vi.spyOn(window, "removeEventListener");
    const { unmount } = mount("/");
    const added = add.mock.calls.filter((c) => c[0] === "popstate").map((c) => c[1]);
    expect(added.length).toBeGreaterThan(0);
    unmount();
    const removed = remove.mock.calls.filter((c) => c[0] === "popstate").map((c) => c[1]);
    for (const fn of added) expect(removed).toContain(fn);
  });
});
