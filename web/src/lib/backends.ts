import type { ProfileDef } from '../types/schemas';

/**
 * The agent backends the daemon accepts, in display order. Mirrors the
 * server's `config.SupportedBackends` (internal/config/backend_command.go).
 */
export const AGENT_BACKENDS = ['claude', 'codex', 'opencode', 'cursor', 'custom'] as const;

export type AgentBackend = (typeof AGENT_BACKENDS)[number];

export const BACKEND_LABELS: Record<AgentBackend, string> = {
  claude: 'Claude',
  codex: 'Codex',
  opencode: 'OpenCode',
  cursor: 'Cursor',
  custom: 'Custom',
};

/**
 * The CLI each backend runs by default — `config.BackendBinary` on the
 * server. Custom has none: its command is whatever speaks the itervox agent
 * protocol.
 */
export const BACKEND_BINARIES: Record<AgentBackend, string> = {
  claude: 'claude',
  codex: 'codex',
  opencode: 'opencode',
  cursor: 'cursor-agent',
  custom: '',
};

export function isAgentBackend(value: unknown): value is AgentBackend {
  return typeof value === 'string' && (AGENT_BACKENDS as readonly string[]).includes(value);
}

/** Display label for a backend; an unknown value is shown as-is. */
export function backendLabel(backend: string): string {
  return isAgentBackend(backend) ? BACKEND_LABELS[backend] : backend;
}

/** The backend whose CLI has this executable basename (`config.BackendForBinary`). */
export function backendForBinary(base: string): AgentBackend | null {
  for (const backend of AGENT_BACKENDS) {
    if (BACKEND_BINARIES[backend] !== '' && BACKEND_BINARIES[backend] === base) return backend;
  }
  return null;
}

/**
 * A backend named by a free-form hint: a backend name, or a command whose
 * text mentions a backend's CLI ("codex exec", "/opt/bin/cursor-agent").
 * Null when the hint names none (a wrapper script).
 */
export function backendFromHint(hint: string | undefined | null): AgentBackend | null {
  const value = (hint ?? '').trim();
  if (value === '') return null;
  if (isAgentBackend(value)) return value;
  const lower = value.toLowerCase();
  // Claude last: "claude" inside a command that names another CLI is a
  // wrapper or a flag value, not the program.
  for (const backend of AGENT_BACKENDS) {
    const bin = BACKEND_BINARIES[backend];
    if (backend !== 'claude' && bin !== '' && lower.includes(bin)) return backend;
  }
  return lower.includes('claude') ? 'claude' : null;
}

/**
 * The backend an issue runs on, for its card / list tag: the running
 * worker's backend, else its profile's backend or command, else the
 * daemon's default — Claude when none names one.
 */
export function resolveIssueBackend(
  profile: string | undefined,
  profileDefs: Record<string, ProfileDef> | undefined,
  runningBackend: string | undefined,
  defaultBackend: string | undefined,
): AgentBackend {
  if (runningBackend) return backendFromHint(runningBackend) ?? 'claude';
  if (profile && profileDefs?.[profile]) {
    const def = profileDefs[profile];
    // Explicit backend field takes priority, then the command.
    const hint = def.backend || def.command || '';
    if (hint) return backendFromHint(hint) ?? 'claude';
  }
  return backendFromHint(defaultBackend) ?? 'claude';
}
