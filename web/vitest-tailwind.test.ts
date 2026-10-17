// MTIX-186: keep the Tailwind 4 build and the braces-free dependency tree.
import { existsSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const manifest = JSON.parse(readFileSync("package.json", "utf8"));
const lock = JSON.parse(readFileSync("package-lock.json", "utf8"));
const lockPackages = lock.packages as Record<string, { version: string }>;

describe("Tailwind 4 build", () => {
  it("declares Tailwind 4 with its own Vite plugin", () => {
    expect(manifest.devDependencies.tailwindcss).toMatch(/^\^4\./);
    expect(manifest.devDependencies["@tailwindcss/vite"]).toMatch(/^\^4\./);
  });

  it("does not declare the PostCSS tooling Tailwind 3 needed", () => {
    expect(manifest.devDependencies.postcss).toBeUndefined();
    expect(manifest.devDependencies.autoprefixer).toBeUndefined();
  });

  it("uses CSS-first configuration", () => {
    expect(existsSync("tailwind.config.js")).toBe(false);
    expect(existsSync("postcss.config.js")).toBe(false);
  });

  it("locks Tailwind 4 and keeps the vulnerable braces chain out of the tree", () => {
    expect(lockPackages["node_modules/tailwindcss"].version).toMatch(/^4\./);
    for (const name of ["braces", "micromatch", "fast-glob"]) {
      expect(lockPackages[`node_modules/${name}`], name).toBeUndefined();
    }
  });
});
