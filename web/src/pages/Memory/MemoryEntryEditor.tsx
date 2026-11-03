// Memory page inline editor (#202): text, type, tags and profiles of one
// entry, saved as a partial update.

import { useId, useState } from 'react';
import { Button } from '../../components/ui/button';
import { useUpdateMemory } from '../../queries/memory';
import {
  MEMORY_MAX_TEXT_BYTES,
  MEMORY_TYPES,
  type MemoryEntry,
  type MemoryType,
} from '../../types/schemas';
import { fieldLabelCls, inputCls, selectCls, textareaCls } from '../Settings/formStyles';
import { TYPE_LABELS, byteLength, splitList } from './memoryModel';

export function MemoryEntryEditor({ entry, onDone }: { entry: MemoryEntry; onDone: () => void }) {
  const uid = useId();
  const update = useUpdateMemory();
  const [text, setText] = useState(entry.text);
  const [type, setType] = useState<MemoryType>(entry.type);
  const [tags, setTags] = useState(entry.tags.join(', '));
  const [profiles, setProfiles] = useState(entry.profiles.join(', '));
  const tooLarge = byteLength(text.trim()) > MEMORY_MAX_TEXT_BYTES;

  return (
    <form
      aria-label="Edit memory"
      className="space-y-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (text.trim() === '' || tooLarge) return;
        update.mutate(
          {
            id: entry.id,
            patch: {
              text: text.trim(),
              type,
              tags: splitList(tags),
              profiles: splitList(profiles),
            },
          },
          { onSuccess: onDone },
        );
      }}
    >
      <div>
        <label className={fieldLabelCls} htmlFor={`${uid}-text`}>
          Text
        </label>
        <textarea
          id={`${uid}-text`}
          className={textareaCls}
          rows={4}
          value={text}
          onChange={(e) => {
            setText(e.target.value);
          }}
        />
        {tooLarge && (
          <p role="alert" className="text-theme-danger-text mt-1 text-xs">
            At most {MEMORY_MAX_TEXT_BYTES} bytes.
          </p>
        )}
      </div>
      <div className="grid gap-3 sm:grid-cols-3">
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
          <label className={fieldLabelCls} htmlFor={`${uid}-tags`}>
            Tags
          </label>
          <input
            id={`${uid}-tags`}
            className={inputCls}
            value={tags}
            onChange={(e) => {
              setTags(e.target.value);
            }}
          />
        </div>
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-profiles`}>
            Profiles
          </label>
          <input
            id={`${uid}-profiles`}
            className={inputCls}
            placeholder="every profile"
            value={profiles}
            onChange={(e) => {
              setProfiles(e.target.value);
            }}
          />
        </div>
      </div>
      <div className="flex gap-2">
        <Button
          type="submit"
          size="sm"
          variant="primary"
          disabled={update.isPending || tooLarge || text.trim() === ''}
        >
          Save
        </Button>
        <Button size="sm" variant="ghost" onClick={onDone}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
