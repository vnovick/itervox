// Memory page, Lessons tab (#203): one proposed lesson with its evidence
// and the review actions — approve into a scope, edit then approve, reject
// (the daemon remembers it and does not propose it again).

import { useId, useState } from 'react';
import { Button } from '../../components/ui/button';
import { useApproveLesson, useRejectLesson } from '../../queries/memory';
import {
  MEMORY_MAX_TEXT_BYTES,
  MEMORY_SCOPES,
  type MemoryEntry,
  type MemoryScope,
} from '../../types/schemas';
import { fieldLabelCls, selectCls, textareaCls } from '../Settings/formStyles';
import { EnforcementCard } from './EnforcementCard';
import { LessonEvidence } from './LessonEvidence';
import { SCOPE_INFO, byteLength } from './memoryModel';

export function LessonCard({ entry }: { entry: MemoryEntry }) {
  const uid = useId();
  const approve = useApproveLesson();
  const reject = useRejectLesson();
  const [scope, setScope] = useState<MemoryScope>(entry.scope);
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(entry.text);
  const busy = approve.isPending || reject.isPending;
  const trimmed = text.trim();
  const invalid = trimmed === '' || byteLength(trimmed) > MEMORY_MAX_TEXT_BYTES;

  const onApprove = () => {
    if (editing && invalid) return;
    approve.mutate({
      id: entry.id,
      scope,
      ...(editing && trimmed !== entry.text ? { text: trimmed } : {}),
    });
  };

  return (
    <li className="px-3 py-3 text-sm" data-testid={`lesson-${entry.id}`}>
      {editing ? (
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-text`}>
            Lesson text
          </label>
          <textarea
            id={`${uid}-text`}
            className={textareaCls}
            rows={3}
            value={text}
            onChange={(e) => {
              setText(e.target.value);
            }}
          />
          {byteLength(trimmed) > MEMORY_MAX_TEXT_BYTES && (
            <p role="alert" className="text-theme-danger-text mt-1 text-xs">
              At most {MEMORY_MAX_TEXT_BYTES} bytes.
            </p>
          )}
        </div>
      ) : (
        <p className="text-theme-text">{entry.text}</p>
      )}
      <p className="text-theme-muted mt-0.5 text-xs">
        Proposed · not used until approved
        {entry.source?.issue ? ` · from ${entry.source.issue}` : ''}
      </p>
      <LessonEvidence entry={entry} />
      <EnforcementCard entry={entry} />
      <div className="mt-3 flex flex-wrap items-end gap-2">
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-scope`}>
            Approve into
          </label>
          <select
            id={`${uid}-scope`}
            className={selectCls}
            value={scope}
            onChange={(e) => {
              setScope(e.target.value as MemoryScope);
            }}
          >
            {MEMORY_SCOPES.map((s) => (
              <option key={s} value={s}>
                {SCOPE_INFO[s].label}
              </option>
            ))}
          </select>
        </div>
        <Button
          size="sm"
          variant="primary"
          disabled={busy || (editing && invalid)}
          onClick={onApprove}
        >
          {editing ? 'Save and approve' : 'Approve'}
        </Button>
        {editing ? (
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              setEditing(false);
              setText(entry.text);
            }}
          >
            Cancel edit
          </Button>
        ) : (
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              setEditing(true);
            }}
          >
            Edit
          </Button>
        )}
        <Button
          size="sm"
          variant="ghost"
          disabled={busy}
          onClick={() => {
            reject.mutate(entry.id);
          }}
        >
          Reject
        </Button>
      </div>
      <p className="text-theme-muted mt-1 text-xs">{SCOPE_INFO[scope].git}</p>
    </li>
  );
}
