import Badge from '../../ui/badge/Badge';
import { EmptyState } from '../../ui/EmptyState';
import { mobileIssueHref, type AttentionItem } from '../../../queries/hubPhone';

// #231 — the phone's needs-attention list: every project's agents waiting
// for an answer (questions and guardrail approvals) and recent run
// failures, one tap from the issue's mobile page.

const KIND_LABEL: Record<AttentionItem['kind'], string> = {
  input: 'Needs you',
  failure: 'Failed',
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
    <section aria-label="Needs attention" className="flex flex-col gap-2">
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
        <ul className="flex flex-col gap-2">
          {items.map((it) => (
            <li key={`${it.project}/${it.identifier}/${it.kind}`}>
              <a
                href={mobileIssueHref(it.project, it.identifier)}
                className="border-theme-line bg-theme-bg-elevated active:bg-theme-panel-strong flex min-h-14 flex-col gap-1 rounded-[var(--radius-sm)] border p-3"
              >
                <span className="flex items-center gap-2">
                  <Badge size="sm" color={it.kind === 'input' ? 'warning' : 'error'}>
                    {KIND_LABEL[it.kind]}
                  </Badge>
                  <span className="text-theme-text font-mono text-sm font-semibold">
                    {it.identifier}
                  </span>
                  <span className="text-theme-muted ml-auto truncate text-xs">{it.project}</span>
                </span>
                {it.text && (
                  <span className="text-theme-text-secondary line-clamp-2 text-sm">{it.text}</span>
                )}
              </a>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
