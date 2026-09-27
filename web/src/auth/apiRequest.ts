import { authedFetch } from './authedFetch';
import { ServerErrorSchema } from './SettingsError';

/**
 * CORE-049 — the single request helper for daemon API calls that need the
 * server's structured error. Built on `authedFetch` (so the bearer header,
 * the 401 → `UnauthorizedError` contract and the AuthGate hand-off are
 * unchanged).
 *
 * - Non-2xx responses throw `ApiError` carrying the server's
 *   `{error:{code,message,field?}}` envelope (see internal/server writeError);
 *   a non-JSON or unrecognised body falls back to `${op} failed: ${status}`.
 * - A 503 whose envelope code is an admission rejection
 *   (`RETRYABLE_503_CODES`) is retried exactly once after `Retry-After`
 *   (clamped). The server only sends those codes when NOTHING was enqueued or
 *   written (CORE-005 orchestrator_busy, CORE-170 settings_reloading, the
 *   deps-override queue), so one retry is safe. Other 503s, other statuses
 *   and transport failures are never retried — the request may have landed.
 * - The response body is read exactly once, here; callers get `status` and
 *   the parsed JSON (`undefined` for an empty or non-JSON body).
 */

/** Error thrown for a failed API response. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string | undefined;
  readonly field: string | undefined;
  /** The parsed response body (undefined when empty or not JSON). */
  readonly body: unknown;
  constructor(message: string, status: number, code?: string, field?: string, body?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.field = field;
    this.body = body;
  }
}

/** 503 codes that mean "rejected before anything happened — safe to resend". */
export const RETRYABLE_503_CODES: readonly string[] = [
  'orchestrator_busy',
  'settings_reloading',
  'deps_override_queue_full',
];

/** Delay used when a retryable 503 carries no usable `Retry-After` header. */
const RETRY_DEFAULT_MS = 1_000;
/**
 * Upper bound on how long a 503 retry will wait, whatever `Retry-After`
 * says — an operator click must not hang behind a hostile/buggy header.
 */
export const ISSUE_CONTROL_RETRY_MAX_MS = 5_000;

/**
 * Converts a `Retry-After` header (delta-seconds or HTTP-date, RFC 9110
 * §10.2.3) into a wait in ms, clamped to [0, ISSUE_CONTROL_RETRY_MAX_MS].
 * Missing or unparsable values fall back to RETRY_DEFAULT_MS.
 */
export function retryAfterDelayMs(header: string | null, nowMs: number = Date.now()): number {
  const clamp = (ms: number) => Math.min(ISSUE_CONTROL_RETRY_MAX_MS, Math.max(0, ms));
  const value = header?.trim() ?? '';
  if (value === '') return RETRY_DEFAULT_MS;
  if (/^\d+$/.test(value)) return clamp(Number(value) * 1_000);
  // Only an IMF-fixdate ("Fri, 25 Sep 2026 12:00:03 GMT") is a date; bare
  // signed/decimal numbers like "-1" are invalid, not years.
  const at = /[a-z]/i.test(value) ? Date.parse(value) : Number.NaN;
  if (Number.isNaN(at)) return RETRY_DEFAULT_MS;
  return clamp(at - nowMs);
}

export interface ApiRequestOptions {
  /** Operation label for the fallback message: `${op} failed: ${status}`. */
  op: string;
  method?: string;
  /** Serialised as the JSON request body (sets Content-Type). */
  json?: unknown;
  headers?: HeadersInit;
  signal?: AbortSignal;
  /** Retry once on an admission-rejected 503. Default true. */
  retryOnBusy?: boolean;
}

export interface ApiResult<T> {
  status: number;
  data: T | undefined;
}

interface Envelope {
  code: string;
  message: string;
  field?: string;
}

/** Reads the body once; returns the parsed JSON value, or undefined. */
async function readJson(res: Response): Promise<unknown> {
  // Tolerate minimal Response doubles ({ ok, status }) used by older tests.
  if (typeof (res as Partial<Response>).text !== 'function') {
    if (typeof (res as Partial<Response>).json === 'function') {
      try {
        return await res.json();
      } catch {
        return undefined;
      }
    }
    return undefined;
  }
  let text: string;
  try {
    text = await res.text();
  } catch {
    return undefined;
  }
  if (text.trim() === '') return undefined;
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return undefined;
  }
}

function toEnvelope(data: unknown): Envelope | undefined {
  const parsed = ServerErrorSchema.safeParse(data);
  if (!parsed.success) return undefined;
  return parsed.data.error;
}

function header(res: Response, name: string): string | null {
  const h = (res as Partial<Response>).headers;
  return h && typeof h.get === 'function' ? h.get(name) : null;
}

function apiErrorFrom(res: Response, data: unknown, op: string): ApiError {
  const envelope = toEnvelope(data);
  if (envelope?.message) {
    return new ApiError(envelope.message, res.status, envelope.code, envelope.field, data);
  }
  return new ApiError(
    `${op} failed: ${String(res.status)}`,
    res.status,
    envelope?.code,
    undefined,
    data,
  );
}

export async function apiRequest<T = unknown>(
  path: string,
  options: ApiRequestOptions,
): Promise<ApiResult<T>> {
  const { op, method, json, signal, retryOnBusy = true } = options;
  const headers = new Headers(options.headers);
  const init: RequestInit = { headers };
  if (method) init.method = method;
  if (signal) init.signal = signal;
  if (json !== undefined) {
    headers.set('Content-Type', 'application/json');
    init.body = JSON.stringify(json);
  }

  let res = await authedFetch(path, init);
  let data = await readJson(res);
  if (
    !res.ok &&
    retryOnBusy &&
    res.status === 503 &&
    RETRYABLE_503_CODES.includes(toEnvelope(data)?.code ?? '')
  ) {
    const delay = retryAfterDelayMs(header(res, 'Retry-After'));
    await new Promise<void>((resolve) => {
      setTimeout(resolve, delay);
    });
    res = await authedFetch(path, init);
    data = await readJson(res);
  }
  if (!res.ok) throw apiErrorFrom(res, data, op);
  return { status: res.status, data: data as T | undefined };
}
