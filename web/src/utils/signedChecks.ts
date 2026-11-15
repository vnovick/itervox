// Signed checks (0.3.3) — display helpers for the issue panel's records.
import type { AttestationRecord } from '../types/schemas';
import { fmtMs } from './format';

/** The first 7 hex digits of a git tree hash, as git abbreviates. */
export function shortTree(hash?: string): string {
  return hash ? hash.slice(0, 7) : '—';
}

/** A check's duration: "850ms" under a second, else fmtMs. */
export function checkDuration(ms: number): string {
  return ms < 1000 ? `${String(ms)}ms` : fmtMs(ms);
}

/** A check's outcome: ok on a clean exit 0, else why it failed. */
export function checkOutcome(r: AttestationRecord): { ok: boolean; label: string } {
  if (r.timedOut) return { ok: false, label: 'timed out' };
  if (r.error) return { ok: false, label: 'failed' };
  if (r.exitCode === 0) return { ok: true, label: 'ok · exit 0' };
  return { ok: false, label: `failed · exit ${String(r.exitCode)}` };
}
