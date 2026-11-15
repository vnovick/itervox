// SkillsCard — the skills inventory on Repo Analysis → Capabilities (#200;
// moved from Settings unchanged in behaviour).
//
// Sections:
//   1. Header + Re-scan
//   2. Recommendations (accordion: click for optimization guidance; Fix)
//   3. Capability tiles + expandable lists
//   4. Per-provider split (claude/codex/shared)
//   5. Context budget bars
//
// Runtime analytics moved to the page's Runtime usage section.

import { useState } from 'react';
import {
  useSkillsInventory,
  useSkillsIssues,
  useSkillsScan,
  useSkillsFix,
} from '../../../queries/skills';
import { ContextBudget } from './ContextBudget';
import { ExpandedSection } from './ExpandedSection';
import { InventoryHeader } from './InventoryHeader';
import { ProviderBreakdown } from './ProviderBreakdown';
import { RecommendationRow } from './RecommendationRow';
import {
  SECTION_KEYS,
  SECTION_LABELS,
  fmtTokens,
  inventoryTotals,
  type FixDescriptor,
  type SectionKey,
} from './skillsFormat';

export function SkillsCard() {
  const { data: inventory, isLoading, error } = useSkillsInventory();
  const { data: issues = [] } = useSkillsIssues();
  const scan = useSkillsScan();
  const fix = useSkillsFix();
  const [expanded, setExpanded] = useState<SectionKey | null>(null);

  function applyFix(issueID: string, f: FixDescriptor) {
    if (f.Destructive) {
      const ok = window.confirm(
        `${f.Label}\n\nThis is a destructive action and will edit configuration. Continue?`,
      );
      if (!ok) return;
    }
    fix.mutate({ issueID, fix: f });
  }

  if (isLoading) {
    return (
      <div className="border-theme-line bg-theme-panel rounded-lg border p-4 text-sm">
        Loading skills inventory…
      </div>
    );
  }
  if (error) {
    return (
      <div className="border-theme-danger-soft bg-theme-danger-soft text-theme-danger-text rounded-lg border p-4 text-sm">
        Failed to load skills inventory: {String(error)}
      </div>
    );
  }
  if (!inventory) {
    return (
      <div className="border-theme-line bg-theme-panel rounded-lg border p-4 text-sm">
        <p className="mb-3">Inventory has not been scanned yet.</p>
        <button
          onClick={() => {
            scan.mutate();
          }}
          disabled={scan.isPending}
          className="bg-theme-accent rounded px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-50"
        >
          {scan.isPending ? 'Scanning…' : 'Run first scan'}
        </button>
      </div>
    );
  }

  const { counts, tokens } = inventoryTotals(inventory);

  return (
    <div className="space-y-6">
      <InventoryHeader
        inventory={inventory}
        scanning={scan.isPending}
        onRescan={() => {
          scan.mutate();
        }}
      />

      {inventory.Partial && (
        <div
          role="status"
          className="border-theme-warning-soft bg-theme-warning-soft text-theme-warning-text rounded-lg border p-4 text-sm"
        >
          <p className="font-medium">Partial inventory</p>
          <p className="mt-1 text-xs">
            {inventory.ScanError || 'One or more scanners failed; showing best-effort results.'}
          </p>
        </div>
      )}

      {/* Recommendations — accordion with optimization help */}
      <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
        <p className="text-theme-text mb-2 text-sm font-medium">
          Recommendations <span className="text-theme-muted text-xs">({issues.length})</span>
        </p>
        <p className="text-theme-muted mb-3 text-[11px]">
          Click any recommendation to expand for optimization guidance.
        </p>
        {issues.length === 0 ? (
          <p className="text-theme-muted text-xs">No issues — your inventory looks clean.</p>
        ) : (
          <ul className="space-y-2">
            {issues.map((issue, idx) => (
              <RecommendationRow
                key={`${issue.ID}-${(issue.Affected ?? []).join(',')}-${String(idx)}`}
                issue={issue}
                onApplyFix={applyFix}
                fixPending={fix.isPending}
              />
            ))}
          </ul>
        )}
      </div>

      {/* Capability catalog */}
      <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
        <p className="text-theme-text mb-2 text-sm font-medium">Capabilities</p>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
          {SECTION_KEYS.map((key) => (
            <button
              key={key}
              onClick={() => {
                setExpanded(expanded === key ? null : key);
              }}
              className={`rounded border px-3 py-2 text-left transition-colors ${
                expanded === key
                  ? 'border-theme-accent bg-theme-accent-soft'
                  : 'border-theme-line hover:bg-[var(--bg-soft)]'
              }`}
            >
              <p className="text-theme-muted text-[10px] font-semibold tracking-wider uppercase">
                {SECTION_LABELS[key]}
              </p>
              <p className="text-theme-text mt-0.5 text-lg font-bold tabular-nums">{counts[key]}</p>
              <p className="text-theme-muted text-[10px]">{fmtTokens(tokens[key])} tok</p>
            </button>
          ))}
        </div>

        {expanded && <ExpandedSection sectionKey={expanded} inventory={inventory} />}
      </div>

      <ProviderBreakdown inventory={inventory} />
      <ContextBudget tokens={tokens} />
    </div>
  );
}
