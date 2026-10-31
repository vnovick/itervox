import { EmptyState } from '../../ui/EmptyState';
import { mobileIssueHref, type AttentionItem } from '../../../queries/hubPhone';

// #231 — the phone's needs-attention list: every project's agents waiting
// for an answer (questions and guardrail approvals) and recent run
// failures, one tap from the issue's mobile page.

const KIND_LABEL: Record<AttentionItem['kind'], string> = {
  input: 'Needs you',
  failure: 'Failed',
};
const KIND_PILL: Record<AttentionItem['kind'], string> = {
  input: 'pill pill-warn',
  failure: 'pill pill-danger',
};

export function NeedsAttentionList({
  items,
  loading = false,
  failedProjects = 0,
}: {
  items: readonly AttentionItem[];
  loading?: boolean;
  failedProjects?: number;
}) {
  return (
    <section aria-label="Needs attention" className="flex flex-col gap-2.5">
      {items.length > 0 && (
        <h2 className="text-theme-text text-[15px] font-semibold">Needs you · {items.length}</h2>
      )}
      {failedProjects > 0 && (
        <p role="alert" className="text-theme-warning-text text-xs">
          {failedProjects === 1
            ? 'One project could not be read.'
            : `${String(failedProjects)} projects could not be read.`}
        </p>
      )}
      {items.length === 0 ? (
        loading ? (
          <p className="text-theme-text-secondary text-sm">Checking your projects…</p>
        ) : (
          <EmptyState
            data-testid="attention-empty"
            title="Nothing needs you"
            description="Questions, approvals and failed runs show up here."
          />
        )
      ) : (
        <ul className="flex flex-col gap-3">
          {items.map((it) => (
            <li key={`${it.project}/${it.identifier}/${it.kind}`}>
              <a
                href={mobileIssueHref(it.project, it.identifier)}
                className="glass-panel flex min-h-14 flex-col gap-1.5 rounded-[20px] px-4 py-3.5 active:brightness-95"
              >
                <span className="flex items-center gap-2">
                  <span className={KIND_PILL[it.kind]}>{KIND_LABEL[it.kind]}</span>
                  <span className="text-theme-text-secondary font-mono text-[13px]">
                    {it.identifier}
                  </span>
                  <span className="text-theme-text-secondary ml-auto truncate text-[13px]">
                    {it.project}
                  </span>
                </span>
                {it.text && (
                  <span className="text-theme-text line-clamp-2 text-base leading-snug font-semibold">
                    {it.text}
                  </span>
                )}
              </a>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
