import { useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from 'react';
import { Link, type To } from 'react-router';
import { STATUS_META, type StatusKey } from '../lib/statusModel';
import { useStatusSummary } from '../hooks/useStatusSummary';
import { useDashboardHref, type DashboardTarget } from '../hooks/useDashboardHref';

// CORE-076 — pill colour per status-model tone (one palette for all pills).
// The `.pill-*` classes live in index.css (tokens.contrast.test.ts measures
// them on the top bar's glass).
const PILL_TONE_CLASS: Record<string, string> = {
  success: 'pill-ok',
  warning: 'pill-warn',
  danger: 'pill-danger',
  info: 'pill-accent',
  neutral: 'pill-neutral',
};

const EMPTY_HIDDEN: readonly number[] = [];

interface PillItem {
  key: string;
  /** What the "+N" pill lists for this pill while it is hidden. */
  text: string;
  render: (hidden: boolean) => ReactNode;
}

/**
 * The indexes of `ref`'s children that wrapped below its first row. The
 * container is one row high with overflow hidden, so those are the pills
 * that do not fit. Measured after every render and on every resize.
 */
function useWrappedChildren(ref: RefObject<HTMLElement | null>): readonly number[] {
  const [hidden, setHidden] = useState<readonly number[]>(EMPTY_HIDDEN);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => {
      const kids = Array.from(el.children) as HTMLElement[];
      const top = kids[0]?.offsetTop ?? 0;
      const next = kids.flatMap((k, i) => (k.offsetTop > top ? [i] : []));
      setHidden((prev) =>
        prev.length === next.length && prev.every((v, i) => v === next[i])
          ? prev
          : next.length === 0
            ? EMPTY_HIDDEN
            : next,
      );
    };
    measure();
    if (typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => {
      ro.disconnect();
    };
  });
  return hidden;
}

// CORE-086 — every count pill links to the dashboard section behind it.
function StatusPill({
  statusKey,
  count,
  to,
  prefix = '',
  title,
  hidden,
}: {
  statusKey: StatusKey;
  count: number;
  to: To;
  prefix?: string;
  title?: string;
  hidden: boolean;
}) {
  const meta = STATUS_META[statusKey];
  return (
    <Link
      to={to}
      data-status-key={statusKey}
      data-status-count={count}
      title={title}
      inert={hidden}
      className={`pill shrink-0 hover:opacity-80 ${PILL_TONE_CLASS[meta.tone] ?? ''}`}
    >
      {prefix}
      {count} {meta.noun}
    </Link>
  );
}

/**
 * The top bar's status: the orchestrator state, then the running, paused,
 * waiting, resuming and retrying counts. One row only: the pills that do not
 * fit fold into a single "+N" pill that names them, so the top bar never
 * wraps.
 */
export function StatusPills() {
  const {
    running,
    paused,
    retrying,
    needsInput,
    resuming,
    headline: headlineKey,
  } = useStatusSummary();
  const dashboardHref = useDashboardHref();
  const rowRef = useRef<HTMLSpanElement>(null);
  const hidden = useWrappedChildren(rowRef);
  const headline = STATUS_META[headlineKey];

  const counted = (
    statusKey: StatusKey,
    count: number,
    section: DashboardTarget,
    title: string,
    prefix = '',
  ): PillItem => ({
    key: statusKey,
    text: `${String(count)} ${STATUS_META[statusKey].noun}`,
    render: (isHidden) => (
      <StatusPill
        key={statusKey}
        statusKey={statusKey}
        count={count}
        to={dashboardHref(section)}
        prefix={prefix}
        title={title}
        hidden={isHidden}
      />
    ),
  });

  const items: PillItem[] = [
    {
      key: 'headline',
      text: headline.label,
      render: () => (
        <span
          key="headline"
          data-testid="header-orchestrator-state"
          className={`pill shrink-0 font-semibold ${PILL_TONE_CLASS[headline.tone] ?? 'pill-neutral'}`}
        >
          {headline.label}
        </span>
      ),
    },
  ];
  if (running > 0) {
    items.push({
      key: 'running',
      text: `${String(running)} ${STATUS_META.running.noun}`,
      render: (isHidden) => (
        <Link
          key="running"
          to={dashboardHref('running-sessions')}
          data-status-key="running"
          data-status-count={running}
          title="Jump to the running sessions"
          inert={isHidden}
          className="text-theme-text flex shrink-0 items-center gap-1 rounded leading-6 underline-offset-2 hover:underline"
        >
          <strong>{running}</strong>
          <span>{STATUS_META.running.noun}</span>
        </Link>
      ),
    });
  }
  if (paused > 0) {
    items.push(counted('paused', paused, 'running-sessions', 'Jump to the paused sessions'));
  }
  if (needsInput > 0) {
    items.push(
      counted('input_required', needsInput, 'attention-inbox', 'Jump to the attention inbox'),
    );
  }
  if (resuming > 0) {
    items.push(
      counted('pending_input_resume', resuming, 'pending-resume', 'Jump to the Resuming panel'),
    );
  }
  if (retrying > 0) {
    items.push(counted('retrying', retrying, 'retry-queue', 'Jump to the retry queue', '↻ '));
  }

  const hiddenSet = new Set(hidden.filter((i) => i < items.length));
  const folded = items.filter((_, i) => hiddenSet.has(i)).map((p) => p.text);

  return (
    // Shrinks before the title next to it (shrink-4): the pills fold, the
    // project name stays readable.
    <span className="flex min-w-0 shrink-[4] items-center gap-2">
      <span
        ref={rowRef}
        data-testid="header-status-pills"
        className="flex h-6 min-w-0 shrink flex-wrap content-start items-center gap-x-2 gap-y-6 overflow-hidden"
      >
        {items.map((p, i) => p.render(hiddenSet.has(i)))}
      </span>
      {folded.length > 0 && (
        <span
          data-testid="header-more-pills"
          title={folded.join('\n')}
          className="pill pill-neutral shrink-0 font-semibold"
        >
          <span aria-hidden="true">+{folded.length}</span>
          <span className="sr-only">{`${String(folded.length)} more: ${folded.join(', ')}`}</span>
        </span>
      )}
    </span>
  );
}
