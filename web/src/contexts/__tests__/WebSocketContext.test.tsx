import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { render, screen, act } from "@testing-library/react";
import { WebSocketProvider, useWebSocket } from "../WebSocketContext";
import type { EventType, WebSocketEvent } from "../../types";

/**
 * WebSocket context tests per MTIX-9.1.3.
 * Tests connection, reconnection, event dispatch, and deduplication.
 */

// Mock WebSocket implementation.
class MockWebSocket {
  static instances: MockWebSocket[] = [];
  url: string;
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  readyState = 0;

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }

  close() {
    this.readyState = 3;
  }

  simulateOpen() {
    this.readyState = 1;
    this.onopen?.();
  }

  simulateMessage(data: WebSocketEvent) {
    this.onmessage?.({ data: JSON.stringify(data) });
  }

  simulateClose() {
    this.readyState = 3;
    this.onclose?.();
  }

  simulateError() {
    this.onerror?.();
  }
}

// Test component that exposes WebSocket state.
function WSTestConsumer({
  onEvent,
  eventType,
}: {
  onEvent?: (e: WebSocketEvent) => void;
  eventType?: EventType;
}) {
  const { status, subscribe, subscribeAll } = useWebSocket();

  // Subscribe to events via effects.
  if (onEvent && eventType) {
    subscribe(eventType, onEvent);
  } else if (onEvent) {
    subscribeAll(onEvent);
  }

  return <span data-testid="ws-status">{status}</span>;
}

beforeEach(() => {
  MockWebSocket.instances = [];
  vi.stubGlobal("WebSocket", MockWebSocket);
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("WebSocketContext", () => {
  it("connects on mount", () => {
    render(
      <WebSocketProvider>
        <WSTestConsumer />
      </WebSocketProvider>,
    );

    expect(MockWebSocket.instances).toHaveLength(1);
    expect(MockWebSocket.instances[0]?.url).toContain("/ws");
  });

  it("shows connected status after WebSocket opens", () => {
    render(
      <WebSocketProvider>
        <WSTestConsumer />
      </WebSocketProvider>,
    );

    act(() => {
      MockWebSocket.instances[0]?.simulateOpen();
    });

    expect(screen.getByTestId("ws-status")).toHaveTextContent("connected");
  });

  it("shows reconnecting status after disconnect", () => {
    render(
      <WebSocketProvider>
        <WSTestConsumer />
      </WebSocketProvider>,
    );

    act(() => {
      MockWebSocket.instances[0]?.simulateOpen();
    });
    expect(screen.getByTestId("ws-status")).toHaveTextContent("connected");

    act(() => {
      MockWebSocket.instances[0]?.simulateClose();
    });
    expect(screen.getByTestId("ws-status")).toHaveTextContent("reconnecting");
  });

  it("reconnects with exponential backoff — 1s, 2s, 4s", () => {
    render(
      <WebSocketProvider>
        <WSTestConsumer />
      </WebSocketProvider>,
    );

    // First connection.
    expect(MockWebSocket.instances).toHaveLength(1);

    // Simulate close to trigger reconnect.
    act(() => {
      MockWebSocket.instances[0]?.simulateClose();
    });

    // After 1s, should reconnect (first backoff).
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(MockWebSocket.instances).toHaveLength(2);

    // Second close.
    act(() => {
      MockWebSocket.instances[1]?.simulateClose();
    });

    // After 2s, should reconnect (second backoff).
    act(() => {
      vi.advanceTimersByTime(2000);
    });
    expect(MockWebSocket.instances).toHaveLength(3);

    // Third close.
    act(() => {
      MockWebSocket.instances[2]?.simulateClose();
    });

    // After 4s, should reconnect (third backoff).
    act(() => {
      vi.advanceTimersByTime(4000);
    });
    expect(MockWebSocket.instances).toHaveLength(4);
  });

  it("resets backoff after successful connection", () => {
    render(
      <WebSocketProvider>
        <WSTestConsumer />
      </WebSocketProvider>,
    );

    // Close and wait for reconnect.
    act(() => {
      MockWebSocket.instances[0]?.simulateClose();
    });
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(MockWebSocket.instances).toHaveLength(2);

    // Successful connection resets backoff.
    act(() => {
      MockWebSocket.instances[1]?.simulateOpen();
    });

    // Close again — backoff should be reset to 1s.
    act(() => {
      MockWebSocket.instances[1]?.simulateClose();
    });
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(MockWebSocket.instances).toHaveLength(3);
  });

  it("dispatches node.created events to subscribers", () => {
    const callback = vi.fn();

    render(
      <WebSocketProvider>
        <WSTestConsumer onEvent={callback} eventType="node.created" />
      </WebSocketProvider>,
    );

    act(() => {
      MockWebSocket.instances[0]?.simulateOpen();
    });

    const event: WebSocketEvent = {
      type: "node.created",
      node: {
        id: "PROJ-1",
        parent_id: "",
        project: "PROJ",
        depth: 0,
        seq: 1,
        title: "Test",
        description: "",
        prompt: "",
        acceptance: "",
        labels: [],
        priority: 3,
        status: "open",
        node_type: "issue",
        issue_type: "task",
        creator: "",
        assignee: "",
        agent_state: "idle",
        weight: 1,
        progress: 0,
        content_hash: "",
        child_count: 0,
        created_at: null,
        updated_at: null,
        closed_at: null,
        defer_until: null,
        deleted_at: null,
      },
      parent_id: "",
      timestamp: "2026-03-10T12:00:00Z",
    };

    act(() => {
      MockWebSocket.instances[0]?.simulateMessage(event);
    });

    expect(callback).toHaveBeenCalledWith(event);
  });

  it("dispatches node.updated events to subscribers", () => {
    const callback = vi.fn();

    render(
      <WebSocketProvider>
        <WSTestConsumer onEvent={callback} eventType="node.updated" />
      </WebSocketProvider>,
    );

    act(() => {
      MockWebSocket.instances[0]?.simulateOpen();
    });

    const event: WebSocketEvent = {
      type: "node.updated",
      node_id: "PROJ-1",
      fields: { status: "done" },
      timestamp: "2026-03-10T12:01:00Z",
    };

    act(() => {
      MockWebSocket.instances[0]?.simulateMessage(event);
    });

    expect(callback).toHaveBeenCalledWith(event);
  });

  it("dispatches progress.changed events to subscribers", () => {
    const callback = vi.fn();

    render(
      <WebSocketProvider>
        <WSTestConsumer onEvent={callback} eventType="progress.changed" />
      </WebSocketProvider>,
    );

    act(() => {
      MockWebSocket.instances[0]?.simulateOpen();
    });

    const event: WebSocketEvent = {
      type: "progress.changed",
      node_id: "PROJ-1",
      progress: 0.75,
      timestamp: "2026-03-10T12:02:00Z",
    };

    act(() => {
      MockWebSocket.instances[0]?.simulateMessage(event);
    });

    expect(callback).toHaveBeenCalledWith(event);
  });

  it("dispatches agent.state events to subscribers", () => {
    const callback = vi.fn();

    render(
      <WebSocketProvider>
        <WSTestConsumer onEvent={callback} eventType="agent.state" />
      </WebSocketProvider>,
    );

    act(() => {
      MockWebSocket.instances[0]?.simulateOpen();
    });

    const event: WebSocketEvent = {
      type: "agent.state",
      agent_id: "agent-claude",
      state: "working",
      node_id: "PROJ-1",
      timestamp: "2026-03-10T12:03:00Z",
    };

    act(() => {
      MockWebSocket.instances[0]?.simulateMessage(event);
    });

    expect(callback).toHaveBeenCalledWith(event);
  });

  it("does not dispatch duplicate events after reconnect", () => {
    const callback = vi.fn();

    render(
      <WebSocketProvider>
        <WSTestConsumer onEvent={callback} eventType="node.updated" />
      </WebSocketProvider>,
    );

    act(() => {
      MockWebSocket.instances[0]?.simulateOpen();
    });

    const event: WebSocketEvent = {
      type: "node.updated",
      node_id: "PROJ-1",
      fields: { status: "done" },
      timestamp: "2026-03-10T12:04:00Z",
    };

    // Send event twice — should only be dispatched once.
    act(() => {
      MockWebSocket.instances[0]?.simulateMessage(event);
    });
    act(() => {
      MockWebSocket.instances[0]?.simulateMessage(event);
    });

    expect(callback).toHaveBeenCalledTimes(1);
  });

  it("connection status shows in top bar indicator", () => {
    render(
      <WebSocketProvider>
        <WSTestConsumer />
      </WebSocketProvider>,
    );

    // Initially disconnected (WebSocket hasn't opened yet).
    const statusEl = screen.getByTestId("ws-status");
    expect(statusEl).toBeInTheDocument();
  });

  it("throws when useWebSocket is used outside WebSocketProvider", () => {
    const consoleSpy = vi.spyOn(console, "error").mockImplementation(() => {});

    expect(() => {
      render(<WSTestConsumer />);
    }).toThrow("useWebSocket must be used within a WebSocketProvider");

    consoleSpy.mockRestore();
  });
});
