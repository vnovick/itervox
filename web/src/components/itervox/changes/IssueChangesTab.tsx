import { useMemo, useState, type KeyboardEvent } from 'react';
import { useIssueDiff, type DiffSource } from '../../../queries/changes';
import { useDiffPrefsStore, type DiffLayout } from '../../../store/diffPrefsStore';
import {
  commentOrder,
  usePendingComments,
  useReviewDraftStore,
  useViewedFiles,
} from '../../../store/reviewDraftStore';
import { useIsNarrow } from '../../../hooks/useIsNarrow';
import { DiffFileList } from './DiffFileList';
import { FilePatch } from './FilePatch';
import { ReviewDraft } from './ReviewDraft';

const SOURCE_LABEL: Record<string, string> = { pr: 'PR files', worktree: 'Live worktree' };

const LAYOUTS: readonly { id: DiffLayout; label: string }[] = [
  { id: 'unified', label: 'Unified' },
  { id: 'split', label: 'Side by side' },
];

function typingIn(target: EventTarget): boolean {
  if (!(target instanceof HTMLElement)) return false;
  const tag = target.tagName;
  return (
    tag === 'TEXTAREA' ||
    tag === 'SELECT' ||
    target.isContentEditable ||
    (tag === 'INPUT' && (target as HTMLInputElement).type !== 'checkbox')
  );
}

/**
 * #215 — the issue detail's "Changes" tab: the worktree's files against
 * the merge base with the base branch, and the diff of the selected file.
 * Lazy-loaded by IssueDetailSlide. Hunks need the operator scope (the
 * daemon refuses them to viewers, whose list is read-only).
 *
 * #238 — once the issue has a PR the daemon serves the PR's files
 * (base...head); the live worktree stays one click away while a run is
 * live.
 *
 * Review and continue — operators collect line and range comments and an
 * overall note into a draft (ReviewDraft) and send it to the agent in one
 * action. The viewer toggles unified / side by side (remembered; unified
 * only at phone width), filters the file list, ticks files viewed, steps
 * through draft comments, and takes keys: j / k next / previous file,
 * n / p next / previous comment.
 */
export default function IssueChangesTab({
  identifier,
  live = false,
  issueState,
}: {
  identifier: string;
  /** The issue's run is live (the review steers it). */
  live?: boolean;
  /** The issue's tracker state; hides "Move to …" when already active. */
  issueState?: string;
}) {
  const [source, setSource] = useState<DiffSource | undefined>(undefined);
  const { data, isLoading, error } = useIssueDiff(identifier, true, source);
  const [selected, setSelected] = useState<string | null>(null);
  const [filter, setFilter] = useState('');
  const [focusComment, setFocusComment] = useState<string | null>(null);
  const storedLayout = useDiffPrefsStore((s) => s.layout);
  const setLayout = useDiffPrefsStore((s) => s.setLayout);
  const narrow = useIsNarrow();
  const layout: DiffLayout = narrow ? 'unified' : storedLayout;
  const viewed = useViewedFiles(identifier);
  const toggleViewed = useReviewDraftStore((s) => s.toggleViewed);
  const comments = usePendingComments(identifier);

  const files = useMemo(() => data?.files ?? [], [data?.files]);
  const shownFiles = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return q ? files.filter((f) => f.path.toLowerCase().includes(q)) : files;
  }, [files, filter]);
  const paths = useMemo(() => files.map((f) => f.path), [files]);
  const ordered = useMemo(
    () => [...comments].sort((a, b) => commentOrder(paths, a, b)),
    [comments, paths],
  );
  const commentCounts = useMemo(() => {
    const m: Record<string, number> = {};
    for (const c of comments) m[c.path] = (m[c.path] ?? 0) + 1;
    return m;
  }, [comments]);

  if (isLoading) return <p className="text-theme-muted text-sm">Loading changes…</p>;
  if (error) return <p className="text-theme-danger-text text-sm">{error.message}</p>;
  if (!data) {
    return (
      <p className="text-theme-muted text-sm italic">
        No worktree for this issue — nothing to diff.
      </p>
    );
  }
  const isPR = data.source === 'pr';
  const review = data.source !== undefined && data.hunksAllowed;
  const shownSource = (data.source ?? 'worktree') as DiffSource;
  const selectable = shownFiles.map((f) => f.path);

  const stepFile = (dir: 1 | -1) => {
    if (selectable.length === 0) return;
    const i = selected ? selectable.indexOf(selected) : -1;
    const next = i < 0 ? (dir === 1 ? 0 : selectable.length - 1) : i + dir;
    if (next < 0 || next >= selectable.length) return;
    setSelected(selectable[next]);
    setFocusComment(null);
  };
  const stepComment = (dir: 1 | -1) => {
    if (ordered.length === 0) return;
    const i = focusComment ? ordered.findIndex((c) => c.id === focusComment) : -1;
    const next =
      i < 0 ? (dir === 1 ? 0 : ordered.length - 1) : (i + dir + ordered.length) % ordered.length;
    const c = ordered[next];
    setSelected(c.path);
    setFocusComment(c.id);
  };
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!data.hunksAllowed || e.metaKey || e.ctrlKey || e.altKey || typingIn(e.target)) return;
    const run: Partial<Record<string, () => void>> = {
      j: () => {
        stepFile(1);
      },
      k: () => {
        stepFile(-1);
      },
      n: () => {
        stepComment(1);
      },
      p: () => {
        stepComment(-1);
      },
    };
    const fn = run[e.key];
    if (!fn) return;
    e.preventDefault();
    fn();
  };

  return (
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions -- j/k/n/p shortcuts bubble up from the focused file list, diff and buttons inside; every action also has a button
    <div className="space-y-3" data-testid="issue-changes-tab" onKeyDown={onKeyDown}>
      {data.sources.length > 1 && (
        <div role="group" aria-label="Diff source" className="flex gap-1 text-xs">
          {data.sources.map((s) => (
            <button
              key={s}
              type="button"
              aria-pressed={shownSource === s}
              onClick={() => {
                setSource(s as DiffSource);
                setSelected(null);
              }}
              className={`rounded px-2 py-0.5 ${shownSource === s ? 'bg-theme-bg-soft text-theme-text font-medium' : 'text-theme-muted hover:opacity-80'}`}
            >
              {SOURCE_LABEL[s] ?? s}
            </button>
          ))}
        </div>
      )}
      <p className="text-theme-muted text-xs">
        {isPR ? (
          <>
            Pull request against <code className="font-mono">{data.base}</code> (head{' '}
            <code className="font-mono">{(data.headSha ?? '').slice(0, 12)}</code>)
          </>
        ) : (
          <>
            Against <code className="font-mono">{data.base}</code> (merge base{' '}
            <code className="font-mono">{data.mergeBase.slice(0, 12)}</code>)
          </>
        )}{' '}
        · {data.files.length} {data.files.length === 1 ? 'file' : 'files'}
        {data.truncated ? ' (list truncated)' : ''}
      </p>
      {data.prError && (
        <p className="text-theme-warning-text text-xs">
          The PR&apos;s files could not be read from GitHub ({data.prError}); showing the worktree.
        </p>
      )}
      {data.hunksAllowed && data.files.length > 0 && (
        <div className="flex flex-wrap items-center gap-2" data-testid="diff-toolbar">
          <input
            type="search"
            aria-label="Filter files"
            placeholder="Filter files"
            value={filter}
            onChange={(e) => {
              setFilter(e.target.value);
            }}
            className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted focus:ring-theme-accent min-w-0 flex-1 rounded-lg border px-2 py-1 text-xs focus:ring-1 focus:outline-none"
          />
          <span className="text-theme-muted text-[11px]" data-testid="viewed-count">
            {Object.keys(viewed).filter((p) => paths.includes(p)).length}/{files.length} viewed
          </span>
          {!narrow && (
            <div role="group" aria-label="Diff layout" className="segmented text-xs">
              {LAYOUTS.map((l) => (
                <button
                  key={l.id}
                  type="button"
                  aria-pressed={layout === l.id}
                  onClick={() => {
                    setLayout(l.id);
                  }}
                  className="px-2.5 py-0.5"
                >
                  {l.label}
                </button>
              ))}
            </div>
          )}
        </div>
      )}
      {data.files.length === 0 ? (
        <p className="text-theme-muted text-sm italic">No changes against the base branch.</p>
      ) : shownFiles.length === 0 ? (
        <p className="text-theme-muted text-xs italic">No file matches the filter.</p>
      ) : (
        <DiffFileList
          files={shownFiles}
          selected={selected}
          onSelect={
            data.hunksAllowed
              ? (p) => {
                  setSelected(p);
                  setFocusComment(null);
                }
              : undefined
          }
          viewed={data.hunksAllowed ? viewed : undefined}
          onToggleViewed={
            data.hunksAllowed
              ? (p) => {
                  toggleViewed(identifier, p);
                }
              : undefined
          }
          commentCounts={commentCounts}
        />
      )}
      {!data.hunksAllowed && data.files.length > 0 && (
        <p className="text-theme-muted text-xs italic">
          File diffs need an operator token; this token can see the file list only.
        </p>
      )}
      {data.hunksAllowed && data.files.length > 0 && (
        <div
          className="text-theme-muted flex flex-wrap items-center gap-2 text-[11px]"
          data-testid="diff-keys"
        >
          <span>
            Keys: <kbd className="tag font-mono">j</kbd>/<kbd className="tag font-mono">k</kbd>{' '}
            file, <kbd className="tag font-mono">n</kbd>/<kbd className="tag font-mono">p</kbd>{' '}
            comment
          </span>
          {ordered.length > 0 && (
            <span className="ml-auto flex gap-1">
              <button
                type="button"
                className="hover:text-theme-accent-text underline-offset-2 hover:underline"
                onClick={() => {
                  stepComment(-1);
                }}
              >
                ← Previous comment
              </button>
              <button
                type="button"
                className="hover:text-theme-accent-text underline-offset-2 hover:underline"
                onClick={() => {
                  stepComment(1);
                }}
              >
                Next comment →
              </button>
            </span>
          )}
        </div>
      )}
      {data.hunksAllowed && selected && (
        <FilePatch
          key={`${shownSource}:${selected}`}
          identifier={identifier}
          file={selected}
          source={source}
          worktreePath={data.worktreePath}
          review={review}
          layout={layout}
          focusCommentId={focusComment}
        />
      )}
      {review && (
        <ReviewDraft
          identifier={identifier}
          live={live}
          issueState={issueState}
          prUrl={data.prUrl}
          prReview={isPR && Boolean(data.prUrl)}
          files={paths}
          onJump={(id, path) => {
            setSelected(path);
            setFocusComment(id);
          }}
        />
      )}
    </div>
  );
}
