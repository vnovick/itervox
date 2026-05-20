/**
 * Daemon helper for Playwright e2e tests (T-31).
 *
 * Spawns the `itervox` Go binary in a per-test temp directory using the
 * embedded quickstart WORKFLOW.md (no API key, no real tracker). Returns
 * the loopback URL the daemon bound to plus the bearer token the
 * dashboard requires for authenticated requests.
 *
 * Prerequisite: `make build` (run by `make e2e`) must have produced the
 * binary at the repo root. The binary embeds `web/dist` via //go:embed, so
 * if you change the React bundle, rebuild before re-running.
 */
import { spawn, ChildProcess } from 'node:child_process';
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export interface Daemon {
  url: string; // e.g. "http://127.0.0.1:54321"
  token: string;
  workdir: string;
  // stop kills the subprocess and removes the temp workdir.
  stop: () => Promise<void>;
}

// ESM-safe equivalent of __dirname. Required because the package.json
// declares "type": "module" — `__dirname` would otherwise be undefined.
const HERE = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(HERE, '..', '..', '..');
const BINARY = resolve(REPO_ROOT, 'itervox');
const QUICKSTART = resolve(REPO_ROOT, 'internal', 'templates', 'quickstart.md');

/**
 * Returns a free TCP port by asking the OS to bind :0 and reading the
 * assigned port number. There's an inherent TOCTOU between the close and
 * the daemon's later bind, but in practice a 50ms gap on loopback is
 * tolerable for test infra.
 */
async function pickPort(): Promise<number> {
  const net = await import('node:net');
  return new Promise<number>((resolveP, rejectP) => {
    const srv = net.createServer();
    srv.unref();
    srv.on('error', rejectP);
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address();
      if (typeof addr === 'string' || addr === null) {
        srv.close();
        rejectP(new Error('failed to acquire free port'));
        return;
      }
      const port = addr.port;
      srv.close(() => {
        resolveP(port);
      });
    });
  });
}

/**
 * Polls /api/v1/health until it returns the expected JSON body or the deadline passes.
 */
async function waitForReady(url: string, token: string, deadlineMs: number): Promise<void> {
  const end = Date.now() + deadlineMs;
  let lastErr: unknown = null;
  while (Date.now() < end) {
    try {
      const res = await fetch(`${url}/api/v1/health`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (res.status === 200) {
        const body = (await res.json()) as { status?: unknown };
        if (body.status === 'ok') return;
        lastErr = new Error(`health body was ${JSON.stringify(body)}`);
      } else {
        lastErr = new Error(`health returned ${String(res.status)}`);
      }
    } catch (err) {
      lastErr = err;
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`daemon did not become ready within ${String(deadlineMs)}ms: ${String(lastErr)}`);
}

/**
 * Boots a fresh daemon. Caller must invoke `daemon.stop()` in afterEach
 * (or use Playwright fixtures that handle teardown automatically).
 */
export async function startDaemon(): Promise<Daemon> {
  const port = await pickPort();
  const token = `e2e-token-${String(Date.now())}-${String(Math.floor(Math.random() * 1e6))}`;

  const workdir = mkdtempSync(join(tmpdir(), 'itervox-e2e-'));
  const workflowPath = join(workdir, 'WORKFLOW.md');

  // Use the quickstart template as the base (memory tracker, loopback host).
  // The template's `server.bind:` field is a stylistic legacy that the
  // current config parser ignores in favor of `server.host:` + `server.port:`,
  // so we replace the entire server block with explicit host/port keys.
  // Also inject a minimal profile under agent.profiles so the Automations
  // settings page is interactive (the "Add Automation" button is disabled
  // when no profiles exist).
  const quickstart = readFileSync(QUICKSTART, 'utf8');
  let workflow = quickstart.replace(
    /^server:\n(?:[ \t]+\S.*\n?)+/m,
    `server:\n  host: "127.0.0.1"\n  port: ${String(port)}\n  allow_unauthenticated_lan: false\n`,
  );
  // Inject a placeholder profile if the quickstart doesn't already define one.
  if (!/^\s*profiles:/m.test(workflow)) {
    workflow = workflow.replace(
      /^(agent:\n(?:[ \t]+\S.*\n)*)/m,
      `$1  profiles:\n    echo:\n      command: "echo profile placeholder"\n`,
    );
  }
  writeFileSync(workflowPath, workflow);

  const proc: ChildProcess = spawn(BINARY, ['-workflow', workflowPath], {
    cwd: workdir,
    env: {
      ...process.env,
      ITERVOX_API_TOKEN: token,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  // Surface daemon stderr in test output for diagnosability when a flow
  // fails — Playwright will capture this in the test artifacts.
  proc.stderr?.on('data', (chunk: Buffer) => {
    process.stderr.write(`[itervox-e2e] ${chunk.toString()}`);
  });

  let exitCode: number | null = null;
  proc.on('exit', (code) => {
    exitCode = code;
  });

  const url = `http://127.0.0.1:${String(port)}`;
  try {
    await waitForReady(url, token, 10_000);
  } catch (err) {
    if (exitCode !== null) {
      throw new Error(
        `daemon exited prematurely with code ${String(exitCode)} during readiness probe: ${String(err)}`,
      );
    }
    proc.kill('SIGKILL');
    rmSync(workdir, { recursive: true, force: true });
    throw err;
  }

  return {
    url,
    token,
    workdir,
    async stop() {
      proc.kill('SIGTERM');
      // Give the daemon ~5s to drain gracefully; force-kill if it lingers.
      await new Promise<void>((resolveP) => {
        const timer = setTimeout(() => {
          proc.kill('SIGKILL');
          resolveP();
        }, 5_000);
        proc.on('exit', () => {
          clearTimeout(timer);
          resolveP();
        });
      });
      rmSync(workdir, { recursive: true, force: true });
    },
  };
}
