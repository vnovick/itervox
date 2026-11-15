// The expanded list under a capability tile: skills, subagents, plugins, MCP
// servers, hooks or instruction docs (#200; moved from Settings → SkillsCard).

import type { SkillsInventory } from '../../../types/schemas';
import { SubagentsList } from './SubagentsList';
import { fmtTokens, labelSource, type SectionKey } from './skillsFormat';

export function ExpandedSection({
  sectionKey,
  inventory,
}: {
  sectionKey: SectionKey;
  inventory: SkillsInventory;
}) {
  switch (sectionKey) {
    case 'subagents':
      return <SubagentsList subagents={inventory.Subagents} />;
    case 'skills': {
      const items = inventory.Skills ?? [];
      return (
        <ul className="border-theme-line mt-3 space-y-1 border-t pt-3">
          {items.map((s, i) => (
            <li
              key={`${s.Name}-${String(i)}`}
              className="flex items-center justify-between gap-2 text-xs"
            >
              <span className="text-theme-text font-mono" title={s.FilePath}>
                {s.Name}
              </span>
              <span className="text-theme-muted truncate">
                {labelSource(s.Source)} · {fmtTokens(s.ApproxTokens)} tok
              </span>
            </li>
          ))}
          {items.length === 0 && (
            <li className="text-theme-muted text-xs">No skills configured.</li>
          )}
        </ul>
      );
    }
    case 'plugins': {
      const items = inventory.Plugins ?? [];
      return (
        <ul className="border-theme-line mt-3 space-y-1 border-t pt-3">
          {items.map((p, i) => (
            <li
              key={`${p.Name}-${String(i)}`}
              className="flex items-center justify-between gap-2 text-xs"
            >
              <span className="text-theme-text font-mono">{p.Name}</span>
              <span className="text-theme-muted">
                {labelSource(p.Source)} · {(p.Skills ?? []).length} skills ·{' '}
                {(p.Hooks ?? []).length} hooks · {fmtTokens(p.ApproxTokens)} tok
              </span>
            </li>
          ))}
          {items.length === 0 && (
            <li className="text-theme-muted text-xs">No plugins configured.</li>
          )}
        </ul>
      );
    }
    case 'mcp': {
      const items = inventory.MCPServers ?? [];
      return (
        <ul className="border-theme-line mt-3 space-y-1 border-t pt-3">
          {items.map((srv, i) => (
            <li
              key={`${srv.Name}-${String(i)}`}
              className="flex items-center justify-between gap-2 text-xs"
            >
              <span className="text-theme-text font-mono">{srv.Name}</span>
              <span className="text-theme-muted">
                {srv.Transport ?? '—'} · {labelSource(srv.Source)}
              </span>
            </li>
          ))}
          {items.length === 0 && (
            <li className="text-theme-muted text-xs">No MCP servers configured.</li>
          )}
        </ul>
      );
    }
    case 'hooks': {
      const items = inventory.Hooks ?? [];
      return (
        <ul className="border-theme-line mt-3 space-y-1 border-t pt-3">
          {items.map((h, i) => (
            <li key={`${h.Event}-${String(i)}`} className="text-xs">
              <span className="text-theme-text font-mono">{h.Event}</span>
              {h.Matcher && <span className="text-theme-muted"> · matcher: {h.Matcher}</span>}
              <span className="text-theme-muted"> · {labelSource(h.Source)}</span>
              <div className="text-theme-muted truncate font-mono text-[10px]">{h.Command}</div>
            </li>
          ))}
          {items.length === 0 && <li className="text-theme-muted text-xs">No hooks configured.</li>}
        </ul>
      );
    }
    case 'instructions': {
      const items = inventory.Instructions ?? [];
      return (
        <ul className="border-theme-line mt-3 space-y-1 border-t pt-3">
          {items.map((d, i) => (
            <li
              key={`${d.FilePath}-${String(i)}`}
              className="flex items-center justify-between gap-2 text-xs"
            >
              <span className="text-theme-text truncate font-mono" title={d.FilePath}>
                {d.FilePath}
              </span>
              <span className="text-theme-muted flex-shrink-0">
                {d.Provider} · {d.Scope} · {fmtTokens(d.ApproxTokens)} tok
              </span>
            </li>
          ))}
          {items.length === 0 && (
            <li className="text-theme-muted text-xs">No instruction docs found.</li>
          )}
        </ul>
      );
    }
  }
}
