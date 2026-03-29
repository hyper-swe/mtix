import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import type { EventType, WebSocketEvent } from "../types";

/** Connection status for the WebSocket. */
export type ConnectionStatus = "connected" | "reconnecting" | "disconnected";

/** Callback type for event subscribers. */
type EventCallback = (event: WebSocketEvent) => void;

interface WebSocketContextValue {
  /** Current connection status. */
  status: ConnectionStatus;
  /** Subscribe to specific event types. Returns unsubscribe function. */
  subscribe: (eventType: EventType, callback: EventCallback) => () => void;
  /** Subscribe to all events. Returns unsubscribe function. */
  subscribeAll: (callback: EventCallback) => () => void;
}

const WebSocketContext = createContext<WebSocketContextValue | null>(null);

/** Maximum reconnect backoff in milliseconds. */
const MAX_BACKOFF_MS = 30_000;

/** Initial reconnect delay in milliseconds. */
const INITIAL_BACKOFF_MS = 1_000;

/**
 * Compute WebSocket URL from current location.
 * In development, Vite proxies /ws to the mtix server.
 */
function getWebSocketURL(): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}/ws/events`;
}

/**
 * WebSocketProvider manages the WebSocket connection to the mtix server.
 * Per FR-UI-17: auto-connects on mount, reconnects with exponential backoff,
 * dispatches events to subscribers via pub/sub.
 */
export function WebSocketProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<ConnectionStatus>("disconnected");
  const wsRef = useRef<WebSocket | null>(null);
  const backoffRef = useRef(INITIAL_BACKOFF_MS);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Subscriber maps: eventType -> Set of callbacks.
  const subscribersRef = useRef<Map<EventType, Set<EventCallback>>>(new Map());
  const allSubscribersRef = useRef<Set<EventCallback>>(new Set());

  // Track processed event IDs to prevent duplicates after reconnect.
  const processedRef = useRef<Set<string>>(new Set());

  const dispatch = useCallback((event: WebSocketEvent) => {
    // Deduplicate events using timestamp + node_id as key.
    const eventKey = `${event.type}:${event.node_id ?? ""}:${event.timestamp ?? ""}`;
    if (processedRef.current.has(eventKey)) return;
    processedRef.current.add(eventKey);

    // Prevent memory leak: cap processed set size.
    if (processedRef.current.size > 10_000) {
      const entries = Array.from(processedRef.current);
      processedRef.current = new Set(entries.slice(entries.length - 5_000));
    }

    // Dispatch to type-specific subscribers.
    const typeSubscribers = subscribersRef.current.get(event.type);
    if (typeSubscribers) {
      for (const cb of typeSubscribers) {
        try {
          cb(event);
        } catch {
          // Subscriber errors must not break the event loop.
        }
      }
    }

    // Dispatch to all-event subscribers.
    for (const cb of allSubscribersRef.current) {
      try {
        cb(event);
      } catch {
        // Subscriber errors must not break the event loop.
      }
    }
  }, []);

  const connect = useCallback(() => {
    // Clean up any existing connection.
    if (wsRef.current) {
      wsRef.current.close();
      wsRef.current = null;
    }

    try {
      const ws = new WebSocket(getWebSocketURL());
      wsRef.current = ws;

      ws.onopen = () => {
        setStatus("connected");
        backoffRef.current = INITIAL_BACKOFF_MS;
      };

      ws.onmessage = (event: MessageEvent) => {
        try {
          const data = JSON.parse(String(event.data)) as WebSocketEvent;
          dispatch(data);
        } catch {
          // Ignore malformed messages.
        }
      };

      ws.onclose = () => {
        wsRef.current = null;
        setStatus("reconnecting");
        scheduleReconnect();
      };

      ws.onerror = () => {
        // onclose will fire after onerror, which triggers reconnect.
      };
    } catch {
      setStatus("reconnecting");
      scheduleReconnect();
    }
  }, [dispatch]);

  const scheduleReconnect = useCallback(() => {
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current);
    }

    const delay = backoffRef.current;
    backoffRef.current = Math.min(delay * 2, MAX_BACKOFF_MS);

    reconnectTimerRef.current = setTimeout(() => {
      reconnectTimerRef.current = null;
      connect();
    }, delay);
  }, [connect]);

  // Connect on mount, clean up on unmount.
  useEffect(() => {
    connect();

    return () => {
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current);
      }
      if (wsRef.current) {
        wsRef.current.close();
        wsRef.current = null;
      }
      setStatus("disconnected");
    };
  }, [connect]);

  const subscribe = useCallback(
    (eventType: EventType, callback: EventCallback): (() => void) => {
      if (!subscribersRef.current.has(eventType)) {
        subscribersRef.current.set(eventType, new Set());
      }
      subscribersRef.current.get(eventType)!.add(callback);

      return () => {
        subscribersRef.current.get(eventType)?.delete(callback);
      };
    },
    [],
  );

  const subscribeAll = useCallback(
    (callback: EventCallback): (() => void) => {
      allSubscribersRef.current.add(callback);
      return () => {
        allSubscribersRef.current.delete(callback);
      };
    },
    [],
  );

  return (
    <WebSocketContext.Provider value={{ status, subscribe, subscribeAll }}>
      {children}
    </WebSocketContext.Provider>
  );
}

/** Hook to access WebSocket context. Throws if used outside WebSocketProvider. */
export function useWebSocket(): WebSocketContextValue {
  const ctx = useContext(WebSocketContext);
  if (!ctx) {
    throw new Error("useWebSocket must be used within a WebSocketProvider");
  }
  return ctx;
}
