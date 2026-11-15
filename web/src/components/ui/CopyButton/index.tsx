import { useState } from 'react';

/** A copy-to-clipboard button for a short value (a command, a digest). */
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
