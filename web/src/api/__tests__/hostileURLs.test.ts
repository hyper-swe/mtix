/** Every ID-bearing API path must encode its opaque ID exactly once. */
import { afterEach, describe, expect, it, vi } from "vitest";
import * as api from "../nodes";
import { hostileFixtures } from "../../test/hostileFixtures";

afterEach(() => vi.unstubAllGlobals());

describe("hostile API URLs", () => {
  it.each(hostileFixtures.ids)("encodes $Name on every ID-bearing endpoint", async ({ Value: id }) => {
    const fetcher = vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({}) });
    vi.stubGlobal("fetch", fetcher);
    const encoded = encodeURIComponent(id);
    const calls: [() => Promise<unknown>, string][] = [
      [() => api.getNode(id), `/nodes/${encoded}`],
      [() => api.getChildren(id), `/nodes/${encoded}/children`],
      [() => api.updateNode(id, {}), `/nodes/${encoded}`],
      [() => api.deleteNode(id), `/nodes/${encoded}`],
      [() => api.getTree(id, 2), `/nodes/${encoded}/tree?depth=2`],
      [() => api.getContext(id), `/context/${encoded}`],
      [() => api.claimNode(id, "TEST-agent"), `/nodes/${encoded}/claim`],
      [() => api.transitionNode(id, "done"), `/nodes/${encoded}/done`],
      [() => api.updatePrompt(id, "text"), `/nodes/${encoded}`],
      [() => api.addAnnotation(id, "text"), `/nodes/${encoded}/comment`],
      [() => api.resolveAnnotation(id, id, true), `/nodes/${encoded}`],
      [() => api.rerunChildren(id, "all"), `/nodes/${encoded}/rerun`],
      [() => api.getActivity(id), `/nodes/${encoded}/activity`],
      [() => api.getDependencies(id), `/deps/${encoded}`],
    ];
    for (const [call, path] of calls) {
      await call();
      expect(fetcher.mock.lastCall?.[0]).toBe(`/api/v1${path}`);
    }
    await api.listNodes({ under: id });
    expect(new URL(String(fetcher.mock.lastCall?.[0]), "http://localhost").searchParams.get("under")).toBe(id);
    await api.searchNodes(id);
    expect(new URL(String(fetcher.mock.lastCall?.[0]), "http://localhost").searchParams.get("under")).toBe(id);
    await api.getStats(id);
    expect(new URL(String(fetcher.mock.lastCall?.[0]), "http://localhost").searchParams.get("scope")).toBe(id);
  });
});
