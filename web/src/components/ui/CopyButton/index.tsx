import { useState } from 'react';

/** A copy-to-clipboard pill for a short value (a command, a digest). */
export function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      aria-label={label}
      className="glass-chip text-theme-text hover:bg-theme-panel-strong px-2.5 py-0.5 text-xs font-medium"
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
