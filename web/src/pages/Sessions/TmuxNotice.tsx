import { useState } from 'react';
import { Banner } from '../../components/ui/Banner';
import type { TmuxStatus } from '../../types/schemas';

// #178 tmux: what the panel says when tmux is not installed on the daemon's
// host. The panel stays read-only for tmux sessions until it is; the daemon
// re-detects tmux every discovery pass, so the warning lifts by itself.

/** The message both notices lead with (also the New session button's reason). */
export const TMUX_MISSING = 'tmux not found — install it to control sessions from here.';

/** A copy-to-clipboard button for a short command. */
export function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      aria-label={label}
      className="border-theme-line hover:bg-theme-bg-soft rounded border px-2 py-0.5 text-xs"
      onClick={() => {
        void navigator.clipboard
          .writeText(text)
          .then(() => {
            setCopied(true);
          })
          .catch(() => {});
      }}
    >
      {copied ? 'Copied' : 'Copy'}
    </button>
  );
}

function InstallHint({ tmux }: { tmux: TmuxStatus }) {
  const hint = tmux.installHint;
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <code className="font-mono" data-testid="tmux-install-hint">
        {hint}
      </code>
      <CopyButton text={hint} label="Copy the tmux install command" />
    </span>
  );
}

/** The banner at the top of the Sessions panel. */
export function TmuxMissingBanner({ tmux }: { tmux: TmuxStatus }) {
  return (
    <Banner tone="warning" data-testid="tmux-missing-banner" className="rounded-lg">
      <span className="mr-2">{TMUX_MISSING}</span>
      <InstallHint tmux={tmux} />
    </Banner>
  );
}

/** The warning that takes the composer's place for a tmux session. */
export function TmuxMissingComposer({ tmux }: { tmux: TmuxStatus }) {
  return (
    <div
      data-testid="tmux-missing-composer"
      className="border-theme-line text-theme-text-secondary space-y-1 rounded-lg border border-dashed p-3 text-sm"
    >
      <p>{TMUX_MISSING} Read-only until then.</p>
      <InstallHint tmux={tmux} />
    </div>
  );
}
