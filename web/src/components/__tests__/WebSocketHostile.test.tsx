/** Real provider JSON dispatch triggers the production Kanban refresh/render path. */
import { useEffect, useState } from "react";
import { PromptEditor } from "../PromptEditor";
import { NodeDetail } from "../NodeDetail";
import type { Node } from "../../types";
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, waitFor } from "@testing-library/react";
import { KanbanView } from "../KanbanView";
import { WebSocketProvider, useWebSocket } from "../../contexts/WebSocketContext";
import { hostileFixtures } from "../../test/hostileFixtures";
import { makeNode, expectNoLiveContent } from "./contentInertHelpers";
import * as api from "../../api";

vi.mock("../../api", () => ({ listNodes: vi.fn() }));

// Exercise JSON event data through the real subscriber contract and detail renderer.
function PushDetail() {
  const { subscribeAll } = useWebSocket();
  const [node, setNode] = useState<Node | null>(null);
  useEffect(() => subscribeAll((event) => {
    if (event.type === "node.created") setNode(event.node);
    if (event.type === "node.updated") setNode((old) => old ? { ...old, ...event.fields } : old);
  }), [subscribeAll]);
  if (!node) return null;
  return <><NodeDetail node={node} children={[]} contextChain={[]} activityEntries={[]}
    activityHasMore={false} dependencies={[]} onLoadMoreActivity={vi.fn()}
    onUpdateTitle={vi.fn()} onStatusChange={vi.fn()} onSavePrompt={vi.fn()}
    onSaveAndRerun={vi.fn()} onAddAnnotation={vi.fn()} onResolveAnnotation={vi.fn()}
    onNavigate={vi.fn()} onCreateChild={vi.fn()} />
    <PromptEditor prompt={node.prompt} annotations={node.annotations ?? []} isOpen
      onSave={vi.fn()} onSaveAndRerun={vi.fn()} onCancel={vi.fn()}
      onAddAnnotation={vi.fn()} onResolveAnnotation={vi.fn()} /></>;
}

class Socket {
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  close() { /* Provider cleanup must not schedule a reconnect. */ }
}
afterEach(() => { vi.unstubAllGlobals(); vi.clearAllMocks(); });

describe("WebSocketHostile", () => {
  it.each([...hostileFixtures.ids, ...hostileFixtures.text])("renders pushes for $Name safely", async ({ Value }) => {
    const sockets: Socket[] = [];
    vi.stubGlobal("WebSocket", class extends Socket {
      constructor() { super(); sockets.push(this); }
    });
    vi.mocked(api.listNodes).mockResolvedValue({ nodes: [], total: 0, has_more: false });
    const { container, unmount } = render(<WebSocketProvider>
      <KanbanView nodeStore={{ updateNode: vi.fn() } as never} /><PushDetail />
    </WebSocketProvider>);
    await waitFor(() => expect(container.querySelector('[data-testid="kanban-column-open"]')).not.toBeNull());
    const node = makeNode({ id: Value, title: Value, description: Value, prompt: Value, status: "open",
      annotations: [{ id: Value, text: Value, author: Value, resolved: false, created_at: "2026-10-28T00:00:00Z" }],
    });
    for (const type of ["node.created", "node.updated"]) {
      const shown = type === "node.updated" ? `${Value} updated` : Value;
      const pushed = { ...node, title: shown, description: shown, prompt: shown,
        annotations: node.annotations?.map((ann) => ({ ...ann, text: shown })),
      };
      vi.mocked(api.listNodes).mockResolvedValue({ nodes: [pushed], total: 1, has_more: false });
      const before = vi.mocked(api.listNodes).mock.calls.length;
      act(() => sockets[0]?.onmessage?.({ data: JSON.stringify({
        type, node_id: Value, node: pushed, fields: pushed, timestamp: "2026-10-28T00:00:00Z",
      }) }));
      await waitFor(() => expect(api.listNodes).toHaveBeenCalledTimes(before + 1));
      await waitFor(() => expect(container.querySelector('[data-node-id]')?.getAttribute("data-node-id")).toBe(Value));
      expect(container.textContent).toContain(Value);
      for (const testId of ["node-title", "description-content", "prompt-display", "annotation-item"]) {
        expect(container.querySelector(`[data-testid="${testId}"]`)?.textContent).toContain(shown);
      }
      expectNoLiveContent(container);
    }
    unmount();
  });

  it("survives malformed and type-confused frames and keeps dispatching", async () => {
    const sockets: Socket[] = [];
    vi.stubGlobal("WebSocket", class extends Socket {
      constructor() { super(); sockets.push(this); }
    });
    vi.mocked(api.listNodes).mockResolvedValue({ nodes: [], total: 0, has_more: false });
    const { container, unmount } = render(<WebSocketProvider>
      <KanbanView nodeStore={{ updateNode: vi.fn() } as never} /><PushDetail />
    </WebSocketProvider>);
    await waitFor(() => expect(container.querySelector('[data-testid="kanban-column-open"]')).not.toBeNull());
    const frames = ["{not json", "null", "42", '"text"', "[]", "",
      JSON.stringify({ type: 42, node_id: 7 }), JSON.stringify({ type: "unknown.event" }),
      JSON.stringify({ type: "node.updated", fields: null })];
    for (const data of frames) {
      expect(() => act(() => sockets[0]?.onmessage?.({ data }))).not.toThrow();
    }
    expect(container.querySelector('[data-testid="node-title"]')).toBeNull();
    const node = makeNode({ id: "OK-1", title: "later frame", status: "open" });
    vi.mocked(api.listNodes).mockResolvedValue({ nodes: [node], total: 1, has_more: false });
    const before = vi.mocked(api.listNodes).mock.calls.length;
    act(() => sockets[0]?.onmessage?.({ data: JSON.stringify({
      type: "node.created", node_id: "OK-1", node, timestamp: "2026-10-28T00:00:00Z",
    }) }));
    await waitFor(() => expect(api.listNodes).toHaveBeenCalledTimes(before + 1));
    unmount();
  });
});
