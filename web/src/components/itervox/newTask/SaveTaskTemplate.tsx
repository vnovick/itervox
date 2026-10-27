import { useState } from 'react';
import { useSaveTaskTemplate } from '../../../queries/tasks';
import { Button } from '../../ui/button';
import { Input } from '../../ui/Input';
import { RequireScope } from '../RequireScope';
import type { TaskDraft } from './newTaskModel';

const NAME_RE = /^[a-z0-9][a-z0-9_-]{0,63}$/;

/**
 * #251 — saves the form as `.itervox/tasks/<name>.md` (admin: it writes a
 * committable file). Placeholders are kept, not filled.
 */
export function SaveTaskTemplate({ draft }: { draft: TaskDraft }) {
  const save = useSaveTaskTemplate();
  const [name, setName] = useState('');
  const valid = NAME_RE.test(name) && draft.title.trim() !== '';
  return (
    <RequireScope scope="admin">
      <div className="border-theme-line flex items-center gap-2 border-t pt-3">
        <Input
          aria-label="Template name"
          placeholder="save as template: name"
          value={name}
          className="max-w-[220px]"
          onChange={(e) => {
            setName(e.target.value.trim().toLowerCase());
          }}
        />
        <Button
          size="sm"
          disabled={!valid || save.isPending}
          onClick={() => {
            save.mutate({
              name,
              title: draft.title,
              instructions: draft.instructions,
              profile: draft.profile || undefined,
              backend: draft.backend || undefined,
              playbook: draft.playbook || undefined,
              baseBranch: draft.baseBranch || undefined,
            });
          }}
        >
          Save as template
        </Button>
      </div>
    </RequireScope>
  );
}
