import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import svgr from "vite-plugin-svgr";

// Minimal Node API shims — vite.config.ts runs in Node at build time, but
// @types/node is not a devDependency of this workspace. Declaring exactly
// what we use (no more) keeps tsc -b happy without adding a transitive
// types package. If we ever start importing more Node APIs here, prefer
// adding @types/node over expanding this list.
declare const process: { cwd(): string; env: Record<string, string | undefined> };
declare const console: { log(...args: unknown[]): void };
declare function require(id: string): unknown;

interface FsLike {
  existsSync(path: string): boolean;
  readFileSync(path: string, encoding: "utf8"): string;
}
interface PathLike {
  resolve(...segments: string[]): string;
  dirname(p: string): string;
}
const fs = require("node:fs") as FsLike;
const path = require("node:path") as PathLike;

const DEFAULT_DAEMON_TARGET = "http://127.0.0.1:8090";

/**
 * resolveDaemonTarget walks upward from the Vite cwd looking for a
 * `.itervox/dashboard_url` file (written by the running itervox daemon at
 * startup — see cmd/itervox/pidfile.go::writeDashboardURLFile). When found,
 * its content is the proxy target. This mitigates the "Vite proxy hardcoded
 * to 8090 but the daemon is on 8091" trap that happens when two itervox
 * projects run in parallel or `server.port` is changed in WORKFLOW.md.
 *
 * Fallback: when no dashboard_url file is reachable, the proxy target stays
 * at DEFAULT_DAEMON_TARGET so a fresh checkout still works.
 *
 * Override: ITERVOX_PROXY_TARGET env var wins over everything, for CI / CI-
 * like environments where the daemon URL is known up-front and walking the
 * filesystem is wasteful.
 */
function resolveDaemonTarget(startDir: string): string {
  const envOverride = process.env.ITERVOX_PROXY_TARGET;
  if (envOverride && envOverride.trim() !== "") {
    return envOverride.trim();
  }
  let dir = startDir;
  for (let i = 0; i < 6; i += 1) {
    const candidate = path.resolve(dir, ".itervox", "dashboard_url");
    if (fs.existsSync(candidate)) {
      try {
        const url = fs.readFileSync(candidate, "utf8").trim();
        if (url) {
          // Strip a trailing slash so /api/* paths join cleanly.
          return url.replace(/\/$/, "");
        }
      } catch {
        // ignore — fall back to default
      }
    }
    const parent = path.dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  return DEFAULT_DAEMON_TARGET;
}

const daemonTarget = resolveDaemonTarget(process.cwd());
if (daemonTarget !== DEFAULT_DAEMON_TARGET) {
  console.log(
    `[itervox] vite proxy → ${daemonTarget} (from .itervox/dashboard_url)`,
  );
}

export default defineConfig({
  plugins: [
    react(),
    svgr({
      svgrOptions: {
        icon: true,
        exportType: "named",
        namedExport: "ReactComponent",
      },
    }),
  ],
  build: {
    outDir: "../internal/server/web/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": {
        target: daemonTarget,
        changeOrigin: true,
        // Disable timeout for SSE (Server-Sent Events) long-lived connections.
        timeout: 0,
        proxyTimeout: 0,
      },
    },
  },
});
