import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "path";

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    port: 5173,
    proxy: {
      // Proxy API calls to the mtix server during development.
      "/api": {
        target: "http://127.0.0.1:6849",
        changeOrigin: true,
      },
      // Proxy WebSocket connections to the mtix server.
      "/ws": {
        target: "ws://127.0.0.1:6849",
        ws: true,
      },
      // Proxy admin endpoints to the mtix server.
      "/admin": {
        target: "http://127.0.0.1:6849",
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks: undefined,
      },
    },
  },
});
