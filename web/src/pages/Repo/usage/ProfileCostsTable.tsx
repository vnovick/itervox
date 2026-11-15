// Per-profile approximate context cost, split by component (#200): what each
// agent profile loads before it reads the issue.

import type { ProfileCost } from '../../../types/schemas';
import { fmtTokens } from '../capabilities/skillsFormat';

const COLUMNS: readonly { key: keyof ProfileCost; label: string }[] = [
  { key: 'InstructionTokens', label: 'Instructions' },
  { key: 'SkillTokens', label: 'Skills' },
  { key: 'HookTokens', label: 'Hooks' },
  { key: 'MCPToolSchemaTokens', label: 'MCP tools' },
  { key: 'WorkflowTemplateTokens', label: 'Template' },
  { key: 'TotalApproxTokens', label: 'Total' },
];

function tokensOf(row: ProfileCost, key: keyof ProfileCost): number {
  const v = row[key];
  return typeof v === 'number' ? v : 0;
}

export function ProfileCostsTable({ costs }: { costs: readonly ProfileCost[] }) {
  return (
    <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
      <p className="text-theme-text mb-2 text-sm font-medium">
        Tokens per profile <span className="text-theme-muted text-xs">(approximate)</span>
      </p>
      {costs.length === 0 ? (
        <p className="text-theme-muted text-xs">
          No per-profile breakdown yet — it appears once profiles are configured and the inventory
          has been scanned.
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[32rem] text-xs" aria-label="Tokens per profile">
            <thead>
              <tr className="text-theme-muted text-left">
                <th scope="col" className="py-1 pr-3 font-medium">
                  Profile
                </th>
                {COLUMNS.map((c) => (
                  <th key={c.key} scope="col" className="py-1 pr-3 text-right font-medium">
                    {c.label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {costs.map((row) => (
                <tr key={row.ProfileName} className="border-theme-line border-t">
                  <th scope="row" className="text-theme-text py-1 pr-3 text-left font-mono">
                    {row.ProfileName}
                  </th>
                  {COLUMNS.map((c) => (
                    <td key={c.key} className="text-theme-muted py-1 pr-3 text-right tabular-nums">
                      {fmtTokens(tokensOf(row, c.key))}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
