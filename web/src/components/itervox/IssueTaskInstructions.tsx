import { useState } from 'react';
import { useSetTaskInstructions } from '../../queries/tasks';
import type { TrackerIssue } from '../../types/schemas';
import MarkdownPanel from './MarkdownPanel';
import { RequireScope } from './RequireScope';

// Plain elements, not the ui/ primitives: the issue detail is in the initial
// bundle, and the primitives' class merger would join it (bundle budget).
const BTN =
  'border-theme-line text-theme-text rounded-[var(--radius-sm)] border px-2 py-1 text-[11px] font-medium disabled:opacity-50';

/**
 * #251 — a New task's custom instructions in the issue detail. They go into
 * the task's prompt after the playbook; an operator can edit them until the
 * task first starts, after which they are the run's record.
 */
export default function IssueTaskInstructions({ issue }: { issue: TrackerIssue }) {
  const task = issue.task;
  const save = useSetTaskInstructions();
  const [editing, setEditing] = useState<string | null>(null);
  if (!task) return null;
  const text = task.instructions ?? '';
  return (
    <div data-testid="issue-task-instructions">
      <h4 className="mb-1 flex items-center gap-2 text-xs font-medium tracking-wider uppercase">
        Task instructions
        <span className="text-theme-muted text-[10px] font-normal normal-case">
          {task.local ? 'local task' : 'tracker issue'}
          {task.baseBranch ? ` · base ${task.baseBranch}` : ''}
          {task.started ? ' · frozen (started)' : ''}
        </span>
      </h4>
      {editing === null ? (
        <>
          {text === '' ? (
            <p className="text-theme-muted text-[12px]">No custom instructions.</p>
          ) : (
            <MarkdownPanel>{text}</MarkdownPanel>
          )}
          {!task.started && (
            <RequireScope scope="operator">
              <button
                type="button"
                className={`${BTN} mt-1`}
                onClick={() => {
                  setEditing(text);
                }}
              >
                Edit instructions
              </button>
            </RequireScope>
          )}
        </>
      ) : (
        <div className="flex flex-col gap-2">
          <textarea
            aria-label="Task instructions"
            rows={6}
            value={editing}
            className="border-theme-line bg-theme-bg-elevated text-theme-text focus:border-theme-accent w-full rounded-[var(--radius-sm)] border px-2.5 py-2 text-sm focus:outline-none"
            onChange={(e) => {
              setEditing(e.target.value);
            }}
          />
          <div className="flex gap-2">
            <button
              type="button"
              className="bg-theme-accent rounded-[var(--radius-sm)] px-2.5 py-1 text-[12px] font-medium text-white disabled:opacity-50"
              disabled={save.isPending}
              onClick={() => {
                save.mutate(
                  { identifier: issue.identifier, instructions: editing },
                  {
                    onSuccess: () => {
                      setEditing(null);
                    },
                  },
                );
              }}
            >
              Save
            </button>
            <button
              type="button"
              className={BTN}
              onClick={() => {
                setEditing(null);
              }}
            >
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
