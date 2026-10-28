import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react() as never],
  // The hostile-text fixture is single-sourced in Go testdata; allow exactly that
  // directory (plus the web root) so Vite can import it (MTIX-187.7.9).
  server: { fs: { allow: [".", "../internal/testutil/testdata"] } },
  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    css: true,
  },
});
