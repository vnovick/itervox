// Memory page: add an entry (#202). New entries are private unless the
// project scope is picked; the daemon redacts secrets and enforces the size
// cap, which the form checks first.

import { useId, useState } from 'react';
import { Button } from '../../components/ui/button';
import { useCreateMemory } from '../../queries/memory';
import {
  MEMORY_MAX_TEXT_BYTES,
  MEMORY_TYPES,
  type MemoryScope,
  type MemoryType,
  type MemoryView,
} from '../../types/schemas';
import {
  fieldLabelCls,
  fieldSurfaceCls,
  inputCls,
  selectCls,
  textareaCls,
} from '../Settings/formStyles';
import { SCOPE_INFO, TYPE_LABELS, byteLength, splitList } from './memoryModel';

export function MemoryNewEntry({ files }: { files: MemoryView['files'] }) {
  const uid = useId();
  const create = useCreateMemory();
  const [open, setOpen] = useState(false);
  const [text, setText] = useState('');
  const [type, setType] = useState<MemoryType>('fact');
  const [scope, setScope] = useState<MemoryScope>('private');
  const [tags, setTags] = useState('');
  const writable = files.filter((f) => f.writable).map((f) => f.scope);
  const size = byteLength(text.trim());
  const tooLarge = size > MEMORY_MAX_TEXT_BYTES;

  if (!open) {
    return (
      <Button
        variant="primary"
        size="sm"
        onClick={() => {
          setScope(writable.includes('private') ? 'private' : 'project');
          setOpen(true);
        }}
      >
        Add memory
      </Button>
    );
  }
  return (
    <form
      className={fieldSurfaceCls}
      aria-label="New memory"
      onSubmit={(e) => {
        e.preventDefault();
        if (text.trim() === '' || tooLarge) return;
        create.mutate(
          { text: text.trim(), type, scope, tags: splitList(tags) },
          {
            onSuccess: () => {
              setText('');
              setTags('');
              setOpen(false);
            },
          },
        );
      }}
    >
      <label className={fieldLabelCls} htmlFor={`${uid}-text`}>
        What should every run remember?
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
      {tooLarge && (
        <p role="alert" className="text-theme-danger-text mt-1 text-xs">
          {size} bytes: at most {MEMORY_MAX_TEXT_BYTES}. Split it into smaller entries.
        </p>
      )}
      <div className="mt-3 grid gap-3 sm:grid-cols-3">
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-type`}>
            Type
          </label>
          <select
            id={`${uid}-type`}
            className={selectCls}
            value={type}
            onChange={(e) => {
              setType(e.target.value as MemoryType);
            }}
          >
            {MEMORY_TYPES.map((t) => (
              <option key={t} value={t}>
                {TYPE_LABELS[t]}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-scope`}>
            Scope
          </label>
          <select
            id={`${uid}-scope`}
            className={selectCls}
            value={scope}
            onChange={(e) => {
              setScope(e.target.value as MemoryScope);
            }}
          >
            {writable.map((s) => (
              <option key={s} value={s}>
                {SCOPE_INFO[s].label}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-tags`}>
            Tags
          </label>
          <input
            id={`${uid}-tags`}
            className={inputCls}
            placeholder="web, ci"
            value={tags}
            onChange={(e) => {
              setTags(e.target.value);
            }}
          />
        </div>
      </div>
      <div className="mt-3 flex gap-2">
        <Button
          type="submit"
          variant="primary"
          size="sm"
          disabled={create.isPending || tooLarge || text.trim() === ''}
        >
          Save
        </Button>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => {
            setOpen(false);
          }}
        >
          Cancel
        </Button>
      </div>
    </form>
  );
}
