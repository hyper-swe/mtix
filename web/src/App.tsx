import { ThemeProvider } from "./contexts/ThemeContext";
import { WebSocketProvider } from "./contexts/WebSocketContext";
import { NavigationProvider } from "./contexts/NavigationContext";
import { Layout } from "./components/Layout";

/**
 * Root application component.
 * Wraps the layout in theme, WebSocket, and navigation providers.
 */
export function App() {
  return (
    <ThemeProvider>
      <WebSocketProvider>
        <NavigationProvider>
          <Layout />
        </NavigationProvider>
      </WebSocketProvider>
    </ThemeProvider>
  );
}
