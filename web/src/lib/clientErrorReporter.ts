/**
 * CORE-048 — web client error reporting.
 *
 * Production UI failures (error boundaries, uncaught window errors, unhandled
 * promise rejections) and snapshot schema drift (CORE-047) are POSTed to the
 * daemon's `/api/v1/client-errors`, where they are redacted, logged, counted
 * (`itervox_client_errors_total`) and added to the RecentFailures ring — so
 * the operator running the daemon sees what the viewer's browser saw.
 *
 * Storm prevention: identical {kind, message, route} reports are deduped for
 * CLIENT_ERROR_DEDUPE_MS, and at most CLIENT_ERROR_MAX_PER_MINUTE reports
 * leave a tab per rolling minute. The server rate-limits as well.
 *
 * Privacy: only kind, message, route (pathname, no query/hash) and a stack
 * truncated to CLIENT_ERROR_STACK_MAX are sent — never request bodies or
 * snapshot contents. The daemon redacts again before logging.
 *
 * The reporter never reports its own failures (a failed POST, including a
 * 401, is swallowed) and skips while the auth gate says there is no usable
 * session (needsToken / serverDown). `allow_unauthenticated` sessions sit at
 * 'authorized' without a token and still report.
 */
import { authedFetch } from '../auth/authedFetch';
import { useAuthStore } from '../auth/authStore';
import { setSchemaFallbackListener } from '../types/schemas';

export type ClientErrorKind = 'render' | 'error' | 'unhandledrejection' | 'schema';

export interface ClientErrorReport {
  kind: ClientErrorKind;
  message: string;
  stack?: string;
}

export const CLIENT_ERROR_DEDUPE_MS = 60_000;
export const CLIENT_ERROR_MAX_PER_MINUTE = 10;
/** Stack bound in UTF-8 bytes (the daemon keeps 2 KiB). */
export const CLIENT_ERROR_STACK_MAX = 2_048;
/** Message bound in UTF-8 bytes (the daemon keeps 1 KiB). */
const CLIENT_ERROR_MESSAGE_MAX = 1_024;
const CLIENT_ERROR_ROUTE_MAX = 256;
/**
 * The daemon's cap on the WHOLE request body, in bytes (BH-M2-3). Field
 * bounds alone are not enough: JSON escaping can grow a control character
 * six-fold, so the serialized body is checked against this too.
 */
export const CLIENT_ERROR_BODY_MAX = 8 * 1024;
const WINDOW_MS = 60_000;

const recentKeys = new Map<string, number>();
let sentAt: number[] = [];
const encoder = new TextEncoder();

function utf8Bytes(s: string): number {
  return encoder.encode(s).byteLength;
}

/**
 * Cuts s to at most maxBytes of UTF-8, on a code-point boundary — iterating
 * by code point never splits a surrogate pair (BH-M2-3: the old UTF-16 slice
 * could, and bounded characters rather than the bytes the daemon counts).
 */
function truncateUtf8(s: string, maxBytes: number): string {
  if (utf8Bytes(s) <= maxBytes) return s;
  let out = '';
  let used = 0;
  for (const cp of s) {
    const n = utf8Bytes(cp);
    if (used + n > maxBytes) break;
    out += cp;
    used += n;
  }
  return out;
}

/** Serialises the report, shrinking stack then message until it fits the body cap. */
function boundedBody(kind: ClientErrorKind, message: string, route: string, stack: string): string {
  let msg = message;
  let stk = stack;
  for (;;) {
    const body = JSON.stringify({ kind, message: msg, route, stack: stk });
    if (utf8Bytes(body) <= CLIENT_ERROR_BODY_MAX) return body;
    if (stk !== '') stk = truncateUtf8(stk, Math.floor(utf8Bytes(stk) / 2));
    else msg = truncateUtf8(msg, Math.floor(utf8Bytes(msg) / 2));
  }
}

function currentRoute(): string {
  try {
    return window.location.pathname;
  } catch {
    return '';
  }
}

/**
 * Sends one report, subject to auth state, dedupe and throttling. Fire and
 * forget: never throws, never rejects.
 */
export function reportClientError(report: ClientErrorReport): void {
  try {
    const status = useAuthStore.getState().status;
    if (status === 'needsToken' || status === 'serverDown') return;

    const message = truncateUtf8(report.message || 'unknown error', CLIENT_ERROR_MESSAGE_MAX);
    const route = truncateUtf8(currentRoute(), CLIENT_ERROR_ROUTE_MAX);
    const now = Date.now();

    for (const [k, at] of recentKeys) {
      if (now - at >= CLIENT_ERROR_DEDUPE_MS) recentKeys.delete(k);
    }
    const key = `${report.kind}\u0000${message}\u0000${route}`;
    if (recentKeys.has(key)) return;

    sentAt = sentAt.filter((t) => now - t < WINDOW_MS);
    if (sentAt.length >= CLIENT_ERROR_MAX_PER_MINUTE) return;

    recentKeys.set(key, now);
    sentAt.push(now);

    const body = boundedBody(
      report.kind,
      message,
      route,
      truncateUtf8(report.stack ?? '', CLIENT_ERROR_STACK_MAX),
    );
    void authedFetch('/api/v1/client-errors', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body,
      keepalive: true,
    }).then(
      (res) => {
        // BH-M2-3: a report the daemon did not accept (429 rate limit, 503
        // queue full, 413) was lost, so release its dedupe key — dedupe
        // exists to stop duplicate DELIVERED reports, and keeping the key
        // would hide the same failure for a minute. The per-minute throttle
        // (already charged) still bounds a retry storm. Chosen over a
        // console log, which the operator running the daemon never sees.
        if (!res.ok) recentKeys.delete(key);
      },
      () => {
        // Never report the reporter's own failure (network, 401). Release
        // the key for the same reason as above.
        recentKeys.delete(key);
      },
    );
  } catch {
    // Reporting must never throw into the caller (an error boundary).
  }
}

function describeReason(reason: unknown): { message: string; stack?: string } {
  if (reason instanceof Error) return { message: reason.message, stack: reason.stack };
  if (typeof reason === 'string') return { message: reason };
  try {
    return { message: JSON.stringify(reason) };
  } catch {
    return { message: String(reason) };
  }
}

/**
 * Installs window `error` / `unhandledrejection` listeners and routes
 * CORE-047 enum fallbacks to the reporter. Returns an uninstall function.
 */
export function installGlobalErrorReporting(): () => void {
  const onError = (ev: ErrorEvent) => {
    const { message, stack } = describeReason(ev.error ?? ev.message);
    reportClientError({ kind: 'error', message: message || ev.message, stack });
  };
  const onRejection = (ev: Event) => {
    const { message, stack } = describeReason((ev as PromiseRejectionEvent).reason);
    reportClientError({ kind: 'unhandledrejection', message, stack });
  };
  window.addEventListener('error', onError);
  window.addEventListener('unhandledrejection', onRejection);
  setSchemaFallbackListener((label) => {
    // The label names the field; the unknown value is not sent.
    reportClientError({
      kind: 'schema',
      message: `snapshot field ${label} carried a value this dashboard does not know; used the fallback`,
    });
  });
  return () => {
    window.removeEventListener('error', onError);
    window.removeEventListener('unhandledrejection', onRejection);
    setSchemaFallbackListener(null);
  };
}

/** Test-only: clears dedupe and throttle state. */
export function __resetClientErrorReporterForTest(): void {
  recentKeys.clear();
  sentAt = [];
}
