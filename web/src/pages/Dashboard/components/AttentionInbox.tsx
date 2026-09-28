import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { useItervoxStore } from '../../../store/itervoxStore';
import {
  useDismissInput,
  usePendingIssueIds,
  useProvideInput,
  useResumeIssue,
  useAcknowledgeFailure,
  useTerminateIssue,
} from '../../../queries/issues';
import { useAttentionCount, useOperatorQueue } from '../../../hooks/useOperatorQueue';
import type { OperatorQueueGroupResult, OperatorQueueItem } from '../../../lib/operatorQueue';
import { ConfirmButton } from '../../../components/ui/button/ConfirmButton';
import { InputReplyBox } from '../../../components/itervox/InputReplyBox';
import { ReviewSourcePill } from '../../../components/itervox/ReviewSourcePill';

// CORE-077 — the attention inbox, pinned at the top of the Dashboard. One
// list of everything that needs the operator (lib/operatorQueue), with the
// actions inline:
//   - needs input: Reply (the shared InputReplyBox — honours inlineInput and
//     useProvideInput's 409 inline_input_enabled refresh) and Dismiss;
//   - paused: Resume, and Discard behind a confirmation;
//   - everything else: Open (the issue slide, or the page that fixes it).
// Rows are never removed optimistically: an item leaves when the next
// snapshot drops it. Resuming rows are read-only and not counted.
// Deliberately not a live region — snapshot pushes would re-announce it; the
// count reaches assistive tech through the heading and the nav link name.

const TONE_STRIPE: Record<OperatorQueueItem['tone'], string> = {
  danger: 'bg-theme-danger',
  warning: 'bg-theme-warning',
  info: 'bg-theme-accent',
};

const ACTION_BUTTON =
  'border-theme-line text-theme-text hover:bg-theme-bg-soft rounded-[var(--radius-sm)] border px-2.5 py-1 text-xs font-medium disabled:opacity-50';

function itemTestKey(item: OperatorQueueItem): string {
  // Stable, human-readable: group + identifier (or the backend / outbox id).
  const [, rest = item.id] = item.id.split(/:(.*)/s);
  const key =
    item.group === 'backend_limited'
      ? rest.replace(/@$/, '')
      : item.group === 'outbox'
        ? rest
        : (item.identifier ?? rest);
  return `attention-item-${item.group}-${key}`;
}

export function AttentionInbox({ onSelectIssue }: { onSelectIssue: (identifier: string) => void }) {
  const queue = useOperatorQueue();
  const attention = useAttentionCount();
  const inlineInput = useItervoxStore((s) => s.snapshot?.inlineInput ?? false);
  const navigate = useNavigate();

  // One reply box open at a time; drafts are kept per issue across switches.
  const [replyOpenFor, setReplyOpenFor] = useState<string | null>(null);
  const [drafts, setDrafts] = useState<Record<string, string>>({});

  // BH-M5-3 — every row shares one instance of each mutation hook, which
  // only tracks its latest call. Drafts clear from the hook-level onSent (it
  // fires for every call) and in-flight state comes from the mutation cache,
  // one Set of identifiers per action.
  const provideInput = useProvideInput({
    onSent: (identifier) => {
      setDrafts((prev) => ({ ...prev, [identifier]: '' }));
    },
  });
  const dismissInput = useDismissInput();
  const resume = useResumeIssue();
  // CORE-175 — acknowledge only when the daemon advertises the endpoint.
  const acknowledge = useAcknowledgeFailure();
  const canAck = useItervoxStore((s) => s.snapshot?.capabilities?.includes('failure_ack') ?? false);
  const discard = useTerminateIssue();
  const sendingIds = usePendingIssueIds('provideInput');
  const dismissingIds = usePendingIssueIds('dismissInput');
  const resumingIds = usePendingIssueIds('resume');
  const discardingIds = usePendingIssueIds('terminate');

  // When the row whose reply box is open leaves the inbox (the daemon's next
  // snapshot after a reply or a dismiss), its controls unmount; if focus was
  // in them it would fall to <body>. Hand it to the inbox heading instead.
  const headingRef = useRef<HTMLElement>(null);
  const needsInputIds = queue.groups.find((g) => g.group === 'needs_input')?.items;
  const replyRowPresent =
    replyOpenFor !== null && !!needsInputIds?.some((i) => i.identifier === replyOpenFor);
  useEffect(() => {
    if (replyOpenFor === null || replyRowPresent) return;
    const active = document.activeElement;
    if (!active || active === document.body) headingRef.current?.focus();
  }, [replyOpenFor, replyRowPresent]);

  if (queue.total === 0) {
    return (
      <p
        ref={(el) => {
          headingRef.current = el;
        }}
        tabIndex={-1}
        data-testid="attention-inbox-empty"
        className="text-theme-text-secondary border-theme-line bg-theme-bg-elevated rounded-[var(--radius-md)] border px-4 py-2 text-xs"
      >
        All caught up — nothing needs you right now.
      </p>
    );
  }

  const open = (item: OperatorQueueItem) => {
    if (item.clickAction.type === 'select-issue') onSelectIssue(item.clickAction.identifier);
    else if (item.clickAction.type === 'navigate') void navigate(item.clickAction.path);
  };

  const renderActions = (item: OperatorQueueItem) => {
    const id = item.identifier ?? '';
    const openButton = item.clickAction.type !== 'none' && (
      <button
        type="button"
        className={ACTION_BUTTON}
        aria-label={`Open ${item.title}`}
        onClick={() => {
          open(item);
        }}
      >
        Open
      </button>
    );
    switch (item.group) {
      case 'needs_input': {
        const expanded = replyOpenFor === id;
        return (
          <>
            <button
              type="button"
              className={ACTION_BUTTON}
              aria-label={`Reply to ${id}`}
              aria-expanded={expanded}
              aria-controls={`attention-reply-${id}`}
              onClick={() => {
                setReplyOpenFor(expanded ? null : id);
              }}
            >
              Reply
            </button>
            {openButton}
          </>
        );
      }
      case 'paused':
        return (
          <>
            <button
              type="button"
              className={ACTION_BUTTON}
              aria-label={`Resume ${id}`}
              disabled={resumingIds.has(id)}
              onClick={() => {
                resume.mutate(id);
              }}
            >
              Resume
            </button>
            <ConfirmButton
              label="Discard"
              ariaLabel={`Discard ${id}`}
              confirmLabel="Yes, discard"
              pendingLabel="Discarding…"
              isPending={discardingIds.has(id)}
              onConfirm={() => {
                discard.mutate(id);
              }}
            />
            {openButton}
          </>
        );
      case 'failed':
        return (
          <>
            {canAck && item.occurredAt && (
              <button
                type="button"
                className={ACTION_BUTTON}
                aria-label={`Acknowledge ${id}`}
                title="Hide this failure until the issue fails again"
                disabled={acknowledge.isPending && acknowledge.variables.identifier === id}
                onClick={() => {
                  acknowledge.mutate({ identifier: id, upTo: item.occurredAt ?? '' });
                }}
              >
                Acknowledge
              </button>
            )}
            {openButton}
          </>
        );
      default:
        return openButton;
    }
  };

  const renderGroup = (group: OperatorQueueGroupResult) => {
    const headingId = `attention-group-${group.group}`;
    return (
      <div key={group.group}>
        <h3
          id={headingId}
          className="text-theme-text-secondary mb-1 text-[11px] font-semibold tracking-[0.06em] uppercase"
        >
          {group.label} · {group.items.length}
        </h3>
        <ul
          aria-labelledby={headingId}
          className="divide-theme-line border-theme-line divide-y rounded-[var(--radius-md)] border"
        >
          {group.items.map((item) => {
            const id = item.identifier ?? '';
            const replyOpen = item.group === 'needs_input' && replyOpenFor === id;
            return (
              <li key={item.id} data-testid={itemTestKey(item)} className="px-3 py-2">
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                  <span
                    aria-hidden="true"
                    className={`block h-6 w-1 flex-shrink-0 rounded-full ${TONE_STRIPE[item.tone]}`}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-theme-text font-mono text-sm font-medium">
                        {item.title}
                      </span>
                      {item.reviewSource && <ReviewSourcePill source={item.reviewSource} />}
                      {item.meta && !item.reviewSource && (
                        <span className="text-theme-text-secondary text-xs">{item.meta}</span>
                      )}
                    </div>
                    {item.subtitle && (
                      <p className="text-theme-text-secondary mt-0.5 truncate text-xs">
                        {item.subtitle}
                      </p>
                    )}
                  </div>
                  <div className="flex flex-shrink-0 flex-wrap items-center gap-1.5">
                    {renderActions(item)}
                  </div>
                </div>
                {replyOpen && (
                  <div id={`attention-reply-${id}`} className="mt-2 space-y-2">
                    <InputReplyBox
                      identifier={id}
                      inlineInput={inlineInput}
                      draft={drafts[id] ?? ''}
                      onDraftChange={(draft) => {
                        setDrafts((prev) => ({ ...prev, [id]: draft }));
                      }}
                      provideInputMutation={provideInput}
                      dismissInputMutation={dismissInput}
                      sending={sendingIds.has(id)}
                      dismissing={dismissingIds.has(id)}
                      nameWithIdentifier
                    />
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      </div>
    );
  };

  return (
    <section
      id="attention-inbox"
      aria-labelledby="attention-inbox-heading"
      data-testid="attention-inbox"
      className="border-theme-line bg-theme-bg-elevated scroll-mt-20 rounded-[var(--radius-lg)] border px-4 py-3"
    >
      <h2
        ref={(el) => {
          headingRef.current = el;
        }}
        tabIndex={-1}
        id="attention-inbox-heading"
        className="text-theme-text mb-2 flex items-center gap-2 text-sm font-semibold"
      >
        Needs attention
        <span
          data-testid="attention-inbox-count"
          className="bg-theme-warning-soft text-theme-warning-text rounded-full px-1.5 py-0.5 text-[10px] font-bold"
        >
          {attention}
        </span>
      </h2>
      <div className="space-y-3">{queue.groups.map(renderGroup)}</div>
    </section>
  );
}
