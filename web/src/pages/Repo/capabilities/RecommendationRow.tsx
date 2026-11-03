// One analyzer recommendation: severity, title and description, an optional
// one-click Fix, and an accordion with optimization guidance (#200; moved
// from Settings → SkillsCard).

import { useState } from 'react';
import { RECOMMENDATION_HELP } from './recommendationHelp';
import { SEVERITY_BG, type FixDescriptor } from './skillsFormat';
import { buttonClass } from '../../../components/ui/button/buttonStyles';
import { cn } from '../../../components/ui/cn';

export interface RecommendationRowIssue {
  ID: string;
  Severity: string;
  Title: string;
  Description: string;
  Affected?: string[] | null;
  Fix?: FixDescriptor | null;
}

interface RecommendationRowProps {
  issue: RecommendationRowIssue;
  onApplyFix: (id: string, f: FixDescriptor) => void;
  fixPending: boolean;
}

export function RecommendationRow({ issue, onApplyFix, fixPending }: RecommendationRowProps) {
  const [open, setOpen] = useState(false);
  const help = RECOMMENDATION_HELP[issue.ID];
  const affected = issue.Affected ?? [];

  // The header is a plain clickable div (no ARIA role) so its accessible
  // name doesn't shadow the inner Fix <button>. A dedicated toggle <button>
  // on the right edge handles keyboard a11y; the rest of the row mirrors
  // the same click for affordance.
  return (
    <li className="glass-card overflow-hidden">
      {/* eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions -- mouse shortcut only; the aria-expanded toggle button inside is the keyboard path */}
      <div
        onClick={() => {
          setOpen((v) => !v);
        }}
        className="flex w-full cursor-pointer items-start justify-between gap-3 px-3.5 py-3 text-left"
      >
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className={`pill pill-sm ${SEVERITY_BG[issue.Severity] ?? SEVERITY_BG.info}`}>
              {issue.Severity}
            </span>
            <span className="text-theme-text text-[13px] font-semibold">{issue.Title}</span>
            <button
              type="button"
              aria-expanded={open}
              aria-label={open ? 'Collapse details' : 'Expand details'}
              onClick={(e) => {
                e.stopPropagation();
                setOpen((v) => !v);
              }}
              className="text-theme-text-secondary hover:bg-theme-subtle ml-auto rounded-full px-1.5 text-[10px]"
            >
              {open ? '▼' : '▶'}
            </button>
          </div>
          <p className="text-theme-text-secondary mt-1 text-[13px]">{issue.Description}</p>
        </div>
        {issue.Fix && (
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              if (issue.Fix) onApplyFix(issue.ID, issue.Fix);
            }}
            disabled={fixPending}
            title={issue.Fix.Destructive ? 'Destructive — confirms before running' : 'Apply fix'}
            className={buttonClass(
              'secondary',
              'xs',
              cn('flex-shrink-0', issue.Fix.Destructive && 'text-theme-danger-text'),
              true,
            )}
          >
            {issue.Fix.Label}
          </button>
        )}
      </div>

      {open && (
        <div className="border-theme-line subtle-fill border-t px-3.5 py-3 text-xs">
          {help ? (
            <>
              <p className="text-theme-text mb-2 font-semibold">What this means</p>
              <p className="text-theme-muted mb-3">{help.what}</p>
              <p className="text-theme-text mb-2 font-semibold">How to optimize</p>
              <ol className="text-theme-muted list-decimal space-y-1 pl-4">
                {help.howToFix.map((step) => (
                  <li key={step}>{step}</li>
                ))}
              </ol>
            </>
          ) : (
            <p className="text-theme-muted italic">No optimization guidance for {issue.ID} yet.</p>
          )}

          {affected.length > 0 && (
            <>
              <p className="text-theme-text mt-3 mb-1 font-semibold">Affected</p>
              <ul className="text-theme-muted font-mono text-[11px]">
                {affected.map((a) => (
                  <li key={a}>· {a}</li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}
    </li>
  );
}
