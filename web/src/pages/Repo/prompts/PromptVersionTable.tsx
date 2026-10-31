// One profile's prompt versions (#200): digest, first and last seen, runs,
// success rate and average duration. Selecting a row filters the run list.

import type { PromptVersionRow } from '../../../types/schemas';
import { fmtMs } from '../../../utils/format';
import { shortDigest } from '../../../utils/promptVersions';
import { fmtRate, fmtSeen } from './promptVersionGroups';

interface PromptVersionTableProps {
  profile: string;
  versions: readonly PromptVersionRow[];
  selected: string | null;
  onSelect: (digest: string | null) => void;
}

export function PromptVersionTable({
  profile,
  versions,
  selected,
  onSelect,
}: PromptVersionTableProps) {
  return (
    <div className="glass-panel px-5 py-[18px]">
      <p className="section-title mb-2 font-mono">{profile}</p>
      <div className="overflow-x-auto">
        <table className="data-table min-w-[36rem]" aria-label={`Prompt versions of ${profile}`}>
          <thead>
            <tr>
              <th scope="col">Version</th>
              <th scope="col">First seen</th>
              <th scope="col">Last seen</th>
              <th scope="col" className="text-right">
                Runs
              </th>
              <th scope="col" className="text-right">
                Success
              </th>
              <th scope="col" className="text-right">
                Avg duration
              </th>
            </tr>
          </thead>
          <tbody>
            {versions.map((v) => {
              const active = selected === v.promptDigest;
              return (
                <tr
                  key={`${profile}-${v.promptDigest}`}
                  className={active ? 'bg-theme-accent-soft' : undefined}
                >
                  <td>
                    <button
                      type="button"
                      aria-pressed={active}
                      title={v.promptDigest}
                      onClick={() => {
                        onSelect(active ? null : v.promptDigest);
                      }}
                      className="text-theme-accent-text font-mono hover:underline"
                    >
                      {shortDigest(v.promptDigest)}
                    </button>
                  </td>
                  <td className="text-theme-text-secondary">{fmtSeen(v.firstSeenAt)}</td>
                  <td className="text-theme-text-secondary">{fmtSeen(v.lastSeenAt)}</td>
                  <td className="text-right tabular-nums">{v.runs}</td>
                  <td className="text-right tabular-nums">{fmtRate(v.successRate)}</td>
                  <td className="text-theme-text-secondary text-right tabular-nums">
                    {fmtMs(v.avgDurationMs ?? 0)}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
