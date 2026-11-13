// #251 — the New task form's model: the draft, `{{var}}` placeholders and
// the request it becomes. Pure, so the rules are testable without a DOM.
// The placeholder syntax matches internal/tasktemplate (Go).

import type { TaskCreateRequest, TaskTemplate } from '../../../types/schemas';

export type TaskWhere = 'local' | 'tracker';

export interface TaskDraft {
  template: string;
  title: string;
  instructions: string;
  profile: string;
  backend: string;
  playbook: string;
  baseBranch: string;
  where: TaskWhere;
}

export const EMPTY_DRAFT: TaskDraft = {
  template: '',
  title: '',
  instructions: '',
  profile: '',
  backend: '',
  playbook: '',
  baseBranch: '',
  where: 'local',
};

/** Matches internal/orchestrator MaxTaskInstructionsBytes. */
export const MAX_INSTRUCTIONS_BYTES = 16 * 1024;

/** Field name → message; `form` for errors that belong to no field. */
export type FieldErrors = Partial<Record<keyof TaskDraft | 'vars' | 'form', string>>;

const varPattern = () => /\{\{\s*([A-Za-z0-9_-]{1,64})\s*\}\}/g;

/** The `{{var}}` names in texts, in order of first appearance. */
export function templateVars(...texts: string[]): string[] {
  const out: string[] = [];
  for (const text of texts) {
    for (const m of text.matchAll(varPattern())) {
      const name = m[1];
      if (!out.includes(name)) out.push(name);
    }
  }
  return out;
}

/** Fills every `{{var}}` that has a non-empty value; the rest stay. */
export function fillVars(text: string, vars: Readonly<Record<string, string>>): string {
  return text.replace(varPattern(), (whole, name: string) => {
    const v: string | undefined = vars[name];
    return v ? v : whole;
  });
}

/** The draft a template starts: its fields, keeping where the task lives. */
export function draftFromTemplate(t: TaskTemplate, where: TaskWhere): TaskDraft {
  return {
    template: t.name,
    title: t.title,
    instructions: t.instructions ?? '',
    profile: t.profile ?? '',
    backend: t.backend ?? '',
    playbook: t.playbook ?? '',
    baseBranch: t.baseBranch ?? '',
    where,
  };
}

/**
 * Validates the draft and builds the request: the title is required, every
 * placeholder needs a value, the instructions are bounded. `where` is forced
 * to the tracker when the project cannot hold local tasks.
 */
export function buildTaskRequest(
  draft: TaskDraft,
  vars: Readonly<Record<string, string>>,
  start: boolean,
  localTasks: boolean,
): { request?: TaskCreateRequest; errors: FieldErrors } {
  const errors: FieldErrors = {};
  const missing = templateVars(draft.title, draft.instructions).filter(
    (name) => (vars[name] ?? '').trim() === '',
  );
  if (missing.length > 0) errors.vars = `Fill in: ${missing.join(', ')}`;
  const title = fillVars(draft.title, vars).trim();
  const instructions = fillVars(draft.instructions, vars).trim();
  if (title === '') errors.title = 'A task needs a title.';
  if (new TextEncoder().encode(instructions).length > MAX_INSTRUCTIONS_BYTES) {
    errors.instructions = `The instructions are longer than ${String(MAX_INSTRUCTIONS_BYTES)} bytes.`;
  }
  if (/\s/.test(draft.baseBranch.trim())) errors.baseBranch = 'A branch name has no spaces.';
  if (Object.keys(errors).length > 0) return { errors };
  const opt = (v: string) => (v.trim() === '' ? undefined : v.trim());
  return {
    errors,
    request: {
      title,
      instructions,
      profile: opt(draft.profile),
      backend: opt(draft.backend),
      playbook: opt(draft.playbook),
      baseBranch: opt(draft.baseBranch),
      where: localTasks ? draft.where : 'tracker',
      start,
    },
  };
}
