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
import {
  operatorToneDotClass,
  type OperatorQueueGroupResult,
  type OperatorQueueItem,
} from '../../../lib/operatorQueue';
import { ConfirmButton } from '../../../components/ui/button/ConfirmButton';
import { InputReplyBox } from '../../../components/itervox/InputReplyBox';
import { ReviewSourcePill } from '../../../components/itervox/ReviewSourcePill';
import { RequireScope } from '../../../components/itervox/RequireScope';
import { plainButtonClass } from '../../../components/ui/button/buttonRecipe';

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

// The group label carries the group's tone; quieter groups stay secondary.
const GROUP_LABEL_TONE: Partial<Record<OperatorQueueGroupResult['group'], string>> = {
  needs_input: 'text-theme-warning-text',
  failed: 'text-theme-danger-text',
};

const ACTION_BUTTON = plainButtonClass('secondary', 'md', { pill: true });
const PRIMARY_ACTION_BUTTON = plainButtonClass('primary', 'md', { pill: true });

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
        className="glass-chip text-theme-text-secondary w-fit px-4 py-2 text-[13px]"
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
              className={PRIMARY_ACTION_BUTTON}
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
          className={`mb-1.5 text-xs font-semibold ${
            GROUP_LABEL_TONE[group.group] ?? 'text-theme-text-secondary'
          }`}
        >
          {group.label} · {group.items.length}
        </h3>
        <ul aria-labelledby={headingId} className="space-y-2">
          {group.items.map((item) => {
            const id = item.identifier ?? '';
            const replyOpen = item.group === 'needs_input' && replyOpenFor === id;
            return (
              <li key={item.id} data-testid={itemTestKey(item)} className="glass-card px-3 py-2.5">
                <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
                  <span
                    aria-hidden="true"
                    className={`status-dot ${operatorToneDotClass(item.tone)}`}
                  />
                  <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2.5 gap-y-0.5">
                    <span className="text-theme-text font-mono text-xs font-medium">
                      {item.title}
                    </span>
                    {item.subtitle && (
                      <span className="text-theme-text min-w-0 truncate text-sm font-medium">
                        {item.subtitle}
                      </span>
                    )}
                    {item.reviewSource && <ReviewSourcePill source={item.reviewSource} />}
                    {item.meta && !item.reviewSource && (
                      <span className="text-theme-muted text-xs">{item.meta}</span>
                    )}
                  </div>
                  <div className="flex flex-shrink-0 flex-wrap items-center gap-1.5">
                    {renderActions(item)}
                  </div>
                </div>
                {replyOpen && (
                  <div id={`attention-reply-${id}`} className="mt-2 space-y-2">
                    <RequireScope
                      scope="operator"
                      fallback={
                        <p className="text-theme-muted text-xs">
                          Replying needs an operator token.
                        </p>
                      }
                    >
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
                    </RequireScope>
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
      className="glass-panel scroll-mt-20 px-4 py-4 sm:px-[18px]"
    >
      <h2
        ref={(el) => {
          headingRef.current = el;
        }}
        tabIndex={-1}
        id="attention-inbox-heading"
        className="text-theme-text mb-2.5 flex items-center gap-2 text-base font-semibold"
      >
        Needs attention
        <span data-testid="attention-inbox-count" className="count-pill">
          {attention}
        </span>
      </h2>
      <div className="space-y-2.5">{queue.groups.map(renderGroup)}</div>
    </section>
  );
}
