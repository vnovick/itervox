import type { AgentBackend } from '../../../lib/backends';

// agent.profiles.<name>.model_server: the base URL of a model server the
// profile's agent talks to instead of its vendor's. The daemon sets the
// variable the profile's backend reads when it runs the agent.

/** Offered as the placeholder; the URL `itervox quickstart` defaults to. */
export const DEFAULT_MODEL_SERVER_URL = 'http://127.0.0.1:8000';

/**
 * The variable each backend's CLI reads for its model server, as the daemon
 * sets it (config.ModelServerEnvVar). A backend missing here takes its
 * server from its own config file, so model_server does not apply to it.
 */
export const MODEL_SERVER_ENV_VARS: Partial<Record<AgentBackend, string>> = {
  claude: 'ANTHROPIC_BASE_URL',
  codex: 'OPENAI_BASE_URL',
};

const URL_HINT = `Enter an http:// or https:// URL, like ${DEFAULT_MODEL_SERVER_URL}.`;

/**
 * Why a model_server value cannot be saved for backend, or null. Mirrors
 * config.CheckModelServerValue: empty, a whole $VAR, or an http(s) URL with
 * a host — and the daemon's backend check.
 */
export function modelServerProblem(value: string, backend: AgentBackend): string | null {
  const v = value.trim();
  if (v === '') return null;
  if (!MODEL_SERVER_ENV_VARS[backend]) {
    return "A model server works with Claude and Codex profiles; set it in this CLI's own config.";
  }
  if (/^\$[A-Za-z_][A-Za-z0-9_]*$/.test(v)) return null;
  if (v.includes('$')) return 'A variable must be the whole value, like $LOCAL_MODEL_URL.';
  let url: URL;
  try {
    url = new URL(v);
  } catch {
    return URL_HINT;
  }
  if ((url.protocol !== 'http:' && url.protocol !== 'https:') || url.hostname === '') {
    return URL_HINT;
  }
  return null;
}
