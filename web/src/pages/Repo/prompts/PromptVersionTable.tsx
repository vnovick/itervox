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
    <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
      <p className="text-theme-text mb-2 font-mono text-sm font-medium">{profile}</p>
      <div className="overflow-x-auto">
        <table
          className="w-full min-w-[36rem] text-xs"
          aria-label={`Prompt versions of ${profile}`}
        >
          <thead>
            <tr className="text-theme-muted text-left">
              <th scope="col" className="py-1 pr-3 font-medium">
                Version
              </th>
              <th scope="col" className="py-1 pr-3 font-medium">
                First seen
              </th>
              <th scope="col" className="py-1 pr-3 font-medium">
                Last seen
              </th>
              <th scope="col" className="py-1 pr-3 text-right font-medium">
                Runs
              </th>
              <th scope="col" className="py-1 pr-3 text-right font-medium">
                Success
              </th>
              <th scope="col" className="py-1 pr-3 text-right font-medium">
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
                  className={`border-theme-line border-t ${active ? 'bg-theme-accent-soft' : ''}`}
                >
                  <td className="py-1 pr-3">
                    <button
                      type="button"
                      aria-pressed={active}
                      title={v.promptDigest}
                      onClick={() => {
                        onSelect(active ? null : v.promptDigest);
                      }}
                      className="text-theme-accent font-mono hover:underline"
                    >
                      {shortDigest(v.promptDigest)}
                    </button>
                  </td>
                  <td className="text-theme-muted py-1 pr-3">{fmtSeen(v.firstSeenAt)}</td>
                  <td className="text-theme-muted py-1 pr-3">{fmtSeen(v.lastSeenAt)}</td>
                  <td className="text-theme-text py-1 pr-3 text-right tabular-nums">{v.runs}</td>
                  <td className="text-theme-text py-1 pr-3 text-right tabular-nums">
                    {fmtRate(v.successRate)}
                  </td>
                  <td className="text-theme-muted py-1 pr-3 text-right tabular-nums">
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
