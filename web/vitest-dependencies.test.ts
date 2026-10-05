// MTIX-114: prevent GHSA-82fw-gwwq-j7x9 from returning through dependency drift.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const manifest = JSON.parse(readFileSync("package.json", "utf8"));
const lock = JSON.parse(readFileSync("package-lock.json", "utf8"));
const family = Object.entries(lock.packages as Record<string, { version: string }>)
  .filter(([path]) => /(?:^|\/)node_modules\/(vitest|@vitest\/[^/]+)$/.test(path));

function expectPatchedVersion(version: string) {
  // Keep the supported stable 4.x line; prereleases have separate advisory ranges.
  expect(version).toMatch(/^4\.\d+\.\d+$/);
  const parts = version.split(".");
  const minor = Number(parts[1]);
  const patch = Number(parts[2]);
  expect(minor > 1 || (minor === 1 && patch >= 11)).toBe(true);
}

describe("Vitest dependency security", () => {
  it.each(["vitest", "@vitest/coverage-v8"])("declares a patched minimum for %s", (name) => {
    const range = manifest.devDependencies[name] as string;
    expect(range).toMatch(/^\^4\.\d+\.\d+$/);
    expectPatchedVersion(range.slice(1));
    expect(lock.packages[""].devDependencies[name]).toBe(range);
  });

  it("locks every Vitest package to a patched matching release", () => {
    expect(lock.lockfileVersion).toBe(3);
    for (const name of ["vitest", "@vitest/coverage-v8", "@vitest/mocker"]) {
      expect(lock.packages[`node_modules/${name}`]).toBeDefined();
    }
    const runnerVersion = lock.packages["node_modules/vitest"].version;
    for (const [path, entry] of family) {
      expectPatchedVersion(entry.version);
      expect(entry.version, path).toBe(runnerVersion);
    }
  });
});
