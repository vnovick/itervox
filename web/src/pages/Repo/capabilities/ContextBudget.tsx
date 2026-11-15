// Approximate context cost per capability kind, as bars (#200; moved from
// Settings → SkillsCard).

import { SECTION_KEYS, SECTION_LABELS, fmtTokens, type SectionKey } from './skillsFormat';

export function ContextBudget({ tokens }: { tokens: Record<SectionKey, number> }) {
  const totalTokens = Object.values(tokens).reduce((s, n) => s + n, 0);
  const maxTokens = Math.max(1, ...Object.values(tokens));
  return (
    <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
      <p className="text-theme-text mb-2 text-sm font-medium">
        Context budget{' '}
        <span className="text-theme-muted text-xs">(approx {fmtTokens(totalTokens)} total)</span>
      </p>
      <div className="space-y-2">
        {SECTION_KEYS.map((key) => {
          const pct = Math.round((tokens[key] / maxTokens) * 100);
          return (
            <div key={key} className="flex items-center gap-2 text-xs">
              <span className="text-theme-muted w-24 flex-shrink-0">{SECTION_LABELS[key]}</span>
              <div className="bg-theme-bg-elevated relative h-3 flex-1 overflow-hidden rounded">
                <div
                  className="bg-theme-accent absolute inset-y-0 left-0"
                  style={{ width: `${String(pct)}%` }}
                />
              </div>
              <span className="text-theme-muted w-12 flex-shrink-0 text-right tabular-nums">
                {fmtTokens(tokens[key])}
              </span>
            </div>
          );
        })}
      </div>
      <p className="text-theme-muted mt-3 text-[10px] italic">
        Token counts are approximate (skill body bytes ÷ 4, 800 × MCP server count, hook command
        bytes × 2 ÷ 4, subagent name + description ÷ 4). Useful for ratio comparisons, not absolute
        claims. Run sessions with <code className="font-mono">CLAUDE_CODE_LOG_DIR</code> set to
        refine the MCP figure with observed tool loads.
      </p>
    </div>
  );
}
