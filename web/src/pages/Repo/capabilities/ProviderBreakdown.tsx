// Skill cost split by provider (claude / codex / shared), inferred from each
// skill's source path (#200; moved from Settings → SkillsCard).

import { useMemo } from 'react';
import type { Skill, SkillsInventory } from '../../../types/schemas';
import { classifyProvider, fmtTokens } from './skillsFormat';

const PROVIDERS = ['claude', 'codex', 'shared'] as const;

export function ProviderBreakdown({ inventory }: { inventory: SkillsInventory }) {
  const stats = useMemo(() => {
    const allSkills: Skill[] = [...(inventory.Skills ?? [])];
    for (const p of inventory.Plugins ?? []) allSkills.push(...(p.Skills ?? []));

    const byProvider = {
      claude: { count: 0, tokens: 0 },
      codex: { count: 0, tokens: 0 },
      shared: { count: 0, tokens: 0 },
    };
    for (const s of allSkills) {
      const provider = classifyProvider(s);
      byProvider[provider].count += 1;
      byProvider[provider].tokens += s.ApproxTokens;
    }
    return byProvider;
  }, [inventory]);

  const total = stats.claude.tokens + stats.codex.tokens + stats.shared.tokens;
  const max = Math.max(1, stats.claude.tokens, stats.codex.tokens, stats.shared.tokens);

  return (
    <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
      <p className="text-theme-text mb-2 text-sm font-medium">
        By provider <span className="text-theme-muted text-xs">(skill cost split)</span>
      </p>
      <div className="space-y-2">
        {PROVIDERS.map((p) => {
          const s = stats[p];
          const pct = Math.round((s.tokens / max) * 100);
          return (
            <div key={p} className="flex items-center gap-2 text-xs">
              <span className="text-theme-muted w-24 flex-shrink-0 capitalize">{p}</span>
              <span className="text-theme-text w-10 flex-shrink-0 tabular-nums">{s.count}</span>
              <div className="bg-theme-bg-elevated relative h-3 flex-1 overflow-hidden rounded">
                <div
                  className="bg-theme-accent absolute inset-y-0 left-0"
                  style={{ width: `${String(pct)}%` }}
                />
              </div>
              <span className="text-theme-muted w-12 flex-shrink-0 text-right tabular-nums">
                {fmtTokens(s.tokens)}
              </span>
            </div>
          );
        })}
      </div>
      <p className="text-theme-muted mt-3 text-[10px] italic">
        Total skill cost: {fmtTokens(total)}. Provider is inferred from the source path:{' '}
        <code className="font-mono">~/.claude/skills/</code> = claude,{' '}
        <code className="font-mono">~/.codex/skills/</code> = codex,{' '}
        <code className="font-mono">~/.agents/skills/</code> = shared (loaded by both).
      </p>
    </div>
  );
}
