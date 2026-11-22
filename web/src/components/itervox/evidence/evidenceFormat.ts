// Evidence report — display helpers shared by the tab's sections.
import type { EvidenceApproval } from '../../../types/schemas';

/** A UTC minute ("2026-11-20 09:00"), or "—" for a missing/invalid time. */
export function evidenceTime(iso?: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '—' : d.toISOString().slice(0, 16).replace('T', ' ');
}

/** The first n characters of a digest. */
export function shortDigest(hash: string | undefined, n = 12): string {
  return hash ? hash.slice(0, n) : '';
}

/** What an approval was for: a held push, a held command, or a question. */
export function approvalSubject(a: EvidenceApproval): string {
  if (a.kind === 'guardrail') return a.push ? 'Held push' : 'Held command';
  return 'Agent question';
}

/** The pill class for a decision. */
export function decisionPill(decision: string): string {
  switch (decision) {
    case 'approved':
    case 'answered':
      return 'pill-ok';
    case 'rejected':
    case 'dismissed':
      return 'pill-danger';
    default:
      return 'pill-warn';
  }
}

/** Who decided, as the caller's scope may see it ("" when hidden). */
export function approver(a: EvidenceApproval): string {
  return [a.actorName, a.actor].filter(Boolean).join(' · ');
}
