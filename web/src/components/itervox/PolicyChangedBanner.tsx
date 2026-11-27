import { useState } from 'react';
import { useItervoxStore } from '../../store/itervoxStore';
import { useScopeAllows } from '../../hooks/useCallerScope';

// The dismissal is per viewer and per change: a newer change shows again.
const DISMISSED_KEY = 'itervox.policyChanged.dismissedAt';

function readDismissed(): string | null {
  try {
    return window.localStorage.getItem(DISMISSED_KEY);
  } catch {
    return null;
  }
}

function writeDismissed(at: string) {
  try {
    window.localStorage.setItem(DISMISSED_KEY, at);
  } catch {
    // storage unavailable: dismissed for this page only
  }
}

/**
 * Approval integrity — a file that governs agents (WORKFLOW.md, the settings
 * and rules layers, playbooks, profiles) changed while runs were active.
 * Names the files and the runs; says when a running unsandboxed agent could
 * have made the change. Operator and admin only; dismissible.
 */
export function PolicyChangedBanner() {
  const alert = useItervoxStore((s) => s.snapshot?.policyChanged ?? null);
  const allowed = useScopeAllows('operator');
  const [dismissed, setDismissed] = useState<string | null>(readDismissed);
  if (!alert || !allowed || dismissed === alert.at) return null;
  const files = alert.files ?? [];
  const runs = alert.runIds ?? [];
  return (
    <div role="alert" className="banner-danger" data-testid="policy-changed-banner">
      <strong className="font-semibold">Policy changed while agents were running:</strong>{' '}
      <span className="font-mono text-xs">
        {files.length > 0 ? files.join(', ') : 'governing files'}
      </span>
      {runs.length > 0 && (
        <span className="text-theme-text-secondary ml-2 text-xs">
          (active runs: {runs.join(', ')})
        </span>
      )}
      {alert.possiblyAgent && (
        <span className="ml-2 font-semibold" data-testid="policy-changed-by-agent">
          Possibly changed by a running agent.
        </span>
      )}
      <button
        type="button"
        className="ml-3 text-xs underline"
        onClick={() => {
          writeDismissed(alert.at);
          setDismissed(alert.at);
        }}
      >
        Dismiss
      </button>
    </div>
  );
}
