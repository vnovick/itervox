// SubagentsList — the expanded "Subagents" section of SkillsCard (#86).
// Lists Claude Code subagents from .claude/agents (project, user) and plugin
// manifests, with their source, model, tools and file size.

import type { Subagent } from '../../types/schemas';

const EMPTY_SUBAGENTS: readonly Subagent[] = [];

function sourceLabel(source: string): string {
  if (source === 'project') return 'project';
  if (source === 'user') return 'user';
  if (source.startsWith('plugin:')) return `plugin ${source.slice('plugin:'.length)}`;
  return source;
}

function fmtTokens(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(n);
}

export function SubagentsList({ subagents }: { subagents?: readonly Subagent[] | null }) {
  const items = subagents ?? EMPTY_SUBAGENTS;
  return (
    <ul className="border-theme-line mt-3 space-y-1 border-t pt-3" aria-label="Subagents">
      {items.map((a) => (
        <li
          key={`${a.Source}-${a.Name}`}
          className="flex items-center justify-between gap-2 text-xs"
          title={a.Description ?? a.FilePath}
        >
          <span className="text-theme-text font-mono" title={a.FilePath}>
            @agent-{a.Name}
          </span>
          <span className="text-theme-muted truncate">
            {sourceLabel(a.Source)}
            {a.Model ? ` · ${a.Model}` : ''}
            {a.Tools && a.Tools.length > 0 ? ` · ${a.Tools.join(', ')}` : ' · all tools'}
            {` · ${fmtTokens(a.ApproxTokens)} tok`}
          </span>
        </li>
      ))}
      {items.length === 0 && (
        <li className="text-theme-muted text-xs">
          No subagents found in .claude/agents or installed plugins.
        </li>
      )}
    </ul>
  );
}
