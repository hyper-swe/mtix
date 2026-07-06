import { ThemeProvider } from "./contexts/ThemeContext";
import { WebSocketProvider } from "./contexts/WebSocketContext";
import { NavigationProvider } from "./contexts/NavigationContext";
import { ProjectProvider } from "./contexts/ProjectContext";
import { Layout } from "./components/Layout";

/**
 * Root application component.
 * Wraps the layout in theme, WebSocket, project, and navigation providers.
 */
export function App() {
  return (
    <ThemeProvider>
      <WebSocketProvider>
        <ProjectProvider>
          <NavigationProvider>
            <Layout />
          </NavigationProvider>
        </ProjectProvider>
      </WebSocketProvider>
    </ThemeProvider>
  );
}
