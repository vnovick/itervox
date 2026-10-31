import { useMemo, useState } from 'react';
import { ApiError } from '../../../auth/apiRequest';
import { UnauthorizedError } from '../../../auth/UnauthorizedError';
import { usePlaybooks } from '../../../queries/playbooks';
import { useCreateTask, useTaskTemplates } from '../../../queries/tasks';
import { useItervoxStore } from '../../../store/itervoxStore';
import type { TaskTemplate } from '../../../types/schemas';
import { EMPTY_PROFILES } from '../../../utils/constants';
import { Button } from '../../ui/button';
import { Input } from '../../ui/Input';
import { Select } from '../../ui/Select';
import { Textarea } from '../../ui/Textarea';
import { NewTaskOptions } from './NewTaskOptions';
import {
  buildTaskRequest,
  draftFromTemplate,
  EMPTY_DRAFT,
  templateVars,
  type FieldErrors,
  type TaskDraft,
} from './newTaskModel';
import { SaveTaskTemplate } from './SaveTaskTemplate';
import { TaskField } from './TaskField';
import { TaskTemplateVars } from './TaskTemplateVars';

const EMPTY_TEMPLATES: readonly TaskTemplate[] = [];
const EMPTY_NAMES: readonly string[] = [];

/** Maps a create error onto the form: the server's field, else the form. */
function errorsFrom(err: unknown): FieldErrors {
  if (err instanceof ApiError) {
    const field = err.field && err.field !== '' ? err.field : 'form';
    return { [field === 'template' ? 'form' : field]: err.message };
  }
  return { form: err instanceof Error ? err.message : 'The task could not be created.' };
}

/**
 * #251 — the New task form: a title, custom instructions (Markdown), where
 * the task lives, and optionally a profile, backend, playbook and base
 * branch. A saved template pre-fills it; its `{{var}}` placeholders get an
 * input each. "Create" leaves the task in the first active state;
 * "Create and start" asks the daemon to dispatch it now (slots, guardrails,
 * budgets and dependencies still apply).
 */
export function NewTaskForm({ onDone }: { onDone: () => void }) {
  const { data: tplData } = useTaskTemplates();
  const { data: pbData } = usePlaybooks();
  const profiles = useItervoxStore((s) => s.snapshot?.availableProfiles ?? EMPTY_PROFILES);
  const templates = tplData?.templates ?? EMPTY_TEMPLATES;
  const localTasks = tplData?.localTasks ?? true;
  const playbooks = useMemo(
    () => (pbData?.enabled ? pbData.playbooks.map((p) => p.name) : EMPTY_NAMES),
    [pbData],
  );
  const create = useCreateTask();
  const [draft, setDraft] = useState<TaskDraft>(EMPTY_DRAFT);
  const [vars, setVars] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<FieldErrors>({});
  const varNames = useMemo(
    () => templateVars(draft.title, draft.instructions),
    [draft.title, draft.instructions],
  );

  const patch = (p: Partial<TaskDraft>) => {
    setDraft((d) => ({ ...d, ...p }));
  };
  const pickTemplate = (name: string) => {
    const t = templates.find((x) => x.name === name);
    setDraft(t ? draftFromTemplate(t, draft.where) : { ...EMPTY_DRAFT, where: draft.where });
    setVars({});
    setErrors({});
  };
  const submit = (start: boolean) => {
    const { request, errors: found } = buildTaskRequest(draft, vars, start, localTasks);
    setErrors(found);
    if (!request) return;
    create.mutate(request, {
      onSuccess: () => {
        setDraft(EMPTY_DRAFT);
        setVars({});
        onDone();
      },
      onError: (err) => {
        if (!(err instanceof UnauthorizedError)) setErrors(errorsFrom(err));
      },
    });
  };

  return (
    <form
      aria-label="New task"
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        submit(false);
      }}
    >
      {templates.length > 0 && (
        <TaskField label="Template">
          <Select
            value={draft.template}
            onChange={(e) => {
              pickTemplate(e.target.value);
            }}
          >
            <option value="">No template</option>
            {templates.map((t) => (
              <option key={t.name} value={t.name} title={t.description}>
                {t.name} — {t.title}
              </option>
            ))}
          </Select>
        </TaskField>
      )}
      <TaskField label="Title" error={errors.title}>
        <Input
          value={draft.title}
          invalid={Boolean(errors.title)}
          placeholder="Upgrade lodash"
          onChange={(e) => {
            patch({ title: e.target.value });
          }}
        />
      </TaskField>
      <TaskField
        label="Custom instructions"
        error={errors.instructions}
        hint="Markdown. Added to this task's prompt after the playbook; editable until it starts."
      >
        <Textarea
          rows={6}
          value={draft.instructions}
          invalid={Boolean(errors.instructions)}
          onChange={(e) => {
            patch({ instructions: e.target.value });
          }}
        />
      </TaskField>
      <TaskTemplateVars
        names={varNames}
        values={vars}
        error={errors.vars}
        onChange={(name, value) => {
          setVars((v) => ({ ...v, [name]: value }));
        }}
      />
      <NewTaskOptions
        draft={draft}
        errors={errors}
        profiles={profiles}
        playbooks={playbooks}
        localTasks={localTasks}
        onChange={patch}
      />
      {errors.form && (
        <p role="alert" className="text-theme-danger-text text-[12px]">
          {errors.form}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="submit" disabled={create.isPending}>
          Create
        </Button>
        <Button
          variant="primary"
          disabled={create.isPending}
          onClick={() => {
            submit(true);
          }}
        >
          Create and start
        </Button>
      </div>
      <SaveTaskTemplate draft={draft} />
    </form>
  );
}
