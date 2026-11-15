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
import { buttonClass } from '../../../components/ui/button/buttonStyles';

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
      <div className="glass-panel text-theme-text-secondary px-5 py-[18px] text-sm">
        Loading skills inventory…
      </div>
    );
  }
  if (error) {
    return <div className="banner-danger">Failed to load skills inventory: {String(error)}</div>;
  }
  if (!inventory) {
    return (
      <div className="glass-panel px-5 py-[18px] text-sm">
        <p className="mb-3">Inventory has not been scanned yet.</p>
        <button
          onClick={() => {
            scan.mutate();
          }}
          disabled={scan.isPending}
          className={buttonClass('primary', 'md', undefined, true)}
        >
          {scan.isPending ? 'Scanning…' : 'Run first scan'}
        </button>
      </div>
    );
  }

  const { counts, tokens } = inventoryTotals(inventory);

  return (
    <div className="space-y-4">
      <InventoryHeader
        inventory={inventory}
        scanning={scan.isPending}
        onRescan={() => {
          scan.mutate();
        }}
      />

      {inventory.Partial && (
        <div role="status" className="banner-warn">
          <p className="font-medium">Partial inventory</p>
          <p className="mt-1 text-xs">
            {inventory.ScanError || 'One or more scanners failed; showing best-effort results.'}
          </p>
        </div>
      )}

      {/* Recommendations — accordion with optimization help */}
      <div className="glass-panel px-5 py-[18px]">
        <p className="section-title mb-1">
          Recommendations <span>({issues.length})</span>
        </p>
        <p className="text-theme-text-secondary mb-3 text-xs">
          Click any recommendation to expand for optimization guidance.
        </p>
        {issues.length === 0 ? (
          <p className="text-theme-text-secondary text-sm">
            No issues — your inventory looks clean.
          </p>
        ) : (
          <ul className="space-y-2.5">
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
      <div className="glass-panel px-5 py-[18px]">
        <p className="section-title mb-3">Capabilities</p>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
          {SECTION_KEYS.map((key) => (
            <button
              key={key}
              onClick={() => {
                setExpanded(expanded === key ? null : key);
              }}
              aria-pressed={expanded === key}
              className="stat-tile"
            >
              <span className="stat-tile-label">{SECTION_LABELS[key]}</span>
              <span className="stat-tile-value">{counts[key]}</span>
              <span className="text-theme-text-secondary text-[11px]">
                {fmtTokens(tokens[key])} tok
              </span>
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
