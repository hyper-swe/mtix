# Web npm audit status (MTIX-175)

Scope: `web/package-lock.json`. Release gate: `npm audit --omit=dev` must report 0 vulnerabilities.

## Fixed

- `source-map-js` (high, GHSA-68fv-2mgg-jv7q): `overrides` in `web/package.json` pins `^1.2.2`; the lockfile resolves 1.2.2.
- `postcss-selector-parser` (moderate, GHSA-rj75-hqrm-r3gf) and its dependent `postcss-nested`: `overrides` pins `^7.1.6`; the lockfile resolves 7.1.6.

No new direct dependency was added; only the `overrides` block and the lockfile changed.

## Remaining, dev-only, not shipped

`npm audit` (with dev dependencies) still reports `braces` (high, GHSA-vfj7-8cjw-p6xm, stack exhaustion through deeply nested brace patterns) and its dependents `micromatch`, `fast-glob`, `chokidar` and `tailwindcss` 3.x. No patched `braces` release exists; the only audit remedy is a breaking move to tailwindcss 4.

Justification for all five:

- Every one of them is a devDependency path (`tailwindcss` build tooling and its file watchers/globbers). `npm audit --omit=dev` reports 0 vulnerabilities, so none is in the production dependency tree.
- The embedded bundle (`internal/web/dist`) is static output; none of these packages is included in it or runs at mtix runtime.
- The only inputs `braces` expands at build time are glob patterns from the repository's own `tailwind.config.js` (`content` globs), written by maintainers, not attacker-controlled. The advisory needs an attacker-supplied, deeply nested pattern.
- A tailwindcss 4 migration is a separate, larger change and out of scope here. It landed in MTIX-186: `tailwindcss` 4 no longer pulls in `braces`, `micromatch`, `fast-glob` or `chokidar`, and `npm audit` (with dev dependencies) reports 0 vulnerabilities.
