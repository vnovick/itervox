import { CopyButton } from '../../ui/CopyButton';
import type { PromptParts } from '../../../types/schemas';
import { shortDigest } from '../../../utils/promptVersions';
import type { NormalisedSession } from './types';

// #186 — what a finished run was prompted with (its prompt version) and what
// operators told it while it ran (delivered Sessions-panel messages).

const PART_LABELS: readonly [keyof PromptParts, string][] = [
  ['soul', 'SOUL.md'],
  ['instructions', 'INSTRUCTIONS.md'],
  ['rules', 'rules'],
  ['memory', 'memory'],
  ['playbook', 'playbook'],
  ['task', 'task instructions'], // #251
  ['template', 'template'],
];

function partsTitle(digest: string, parts?: PromptParts): string {
  const lines = [`prompt digest ${digest}`];
  for (const [key, label] of PART_LABELS) {
    lines.push(`${label}: ${parts?.[key] ?? '—'}`);
  }
  return lines.join('\n');
}

function steerTime(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleTimeString();
}

const SECTION_LABEL = 'text-theme-text text-[13px] font-semibold';

export function RunProvenance({ run }: { run: NormalisedSession }) {
  const steers = run.steers ?? [];
  const dropped = run.steersDropped ?? 0;
  return (
    <section
      data-testid="run-provenance"
      aria-label="Run provenance"
      className="glass-card flex-shrink-0 px-4 py-3 text-xs"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className={SECTION_LABEL}>Prompt version</span>
        {run.promptDigest ? (
          <>
            <code
              data-testid="run-prompt-digest"
              className="text-theme-text font-mono"
              title={partsTitle(run.promptDigest, run.promptParts)}
            >
              {shortDigest(run.promptDigest)}
            </code>
            <CopyButton text={run.promptDigest} label="Copy the full prompt digest" />
          </>
        ) : (
          <span className="text-theme-muted italic">not recorded</span>
        )}
        {run.runId && <span className="text-theme-muted font-mono text-[11px]">{run.runId}</span>}
      </div>
      <div className="mt-2">
        <span className={SECTION_LABEL}>Operator messages</span>
        {steers.length === 0 ? (
          <p className="text-theme-muted mt-1 italic">None delivered to this run</p>
        ) : (
          <ol
            data-testid="run-steers"
            className="mt-1 flex max-h-32 flex-col gap-1 overflow-y-auto"
          >
            {steers.map((s, i) => (
              <li key={`${s.at}|${String(i)}`} className="flex items-start gap-2">
                <time dateTime={s.at} className="text-theme-muted shrink-0 font-mono text-[11px]">
                  {steerTime(s.at)}
                </time>
                <span
                  className="pill pill-neutral shrink-0 px-1.5 py-0 text-[11px]"
                  title={
                    s.delivery === 'live'
                      ? 'Written into the running turn'
                      : 'Delivered as the next resumed turn'
                  }
                >
                  {s.delivery}
                </span>
                <span className="text-theme-text break-words whitespace-pre-wrap">{s.text}</span>
              </li>
            ))}
          </ol>
        )}
        {dropped > 0 && (
          <p className="text-theme-muted mt-1 text-[11px]">
            {dropped} earlier message{dropped === 1 ? '' : 's'} not kept
          </p>
        )}
      </div>
    </section>
  );
}
