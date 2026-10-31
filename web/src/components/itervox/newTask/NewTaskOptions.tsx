import { Input } from '../../ui/Input';
import { Select } from '../../ui/Select';
import type { FieldErrors, TaskDraft, TaskWhere } from './newTaskModel';
import { TaskField } from './TaskField';

const BACKENDS = ['claude', 'codex'] as const;

interface NewTaskOptionsProps {
  draft: TaskDraft;
  errors: FieldErrors;
  profiles: readonly string[];
  playbooks: readonly string[];
  localTasks: boolean;
  onChange: (patch: Partial<TaskDraft>) => void;
}

/**
 * #251 — where the task lives and how it runs: local issue or tracker issue,
 * profile, backend, playbook and base branch. Every choice is optional; empty
 * means the daemon's default.
 */
export function NewTaskOptions({
  draft,
  errors,
  profiles,
  playbooks,
  localTasks,
  onChange,
}: NewTaskOptionsProps) {
  const where: TaskWhere = localTasks ? draft.where : 'tracker';
  return (
    <div className="flex flex-col gap-3">
      <fieldset>
        <legend className="text-theme-text-secondary mb-1 text-xs font-medium">Create as</legend>
        <div className="flex flex-wrap gap-4 text-[13px]">
          <label className="flex items-center gap-1.5">
            <input
              type="radio"
              name="new-task-where"
              value="local"
              checked={where === 'local'}
              disabled={!localTasks}
              onChange={() => {
                onChange({ where: 'local' });
              }}
            />
            Local issue (Itervox only)
          </label>
          <label className="flex items-center gap-1.5">
            <input
              type="radio"
              name="new-task-where"
              value="tracker"
              checked={where === 'tracker'}
              onChange={() => {
                onChange({ where: 'tracker' });
              }}
            />
            Tracker issue
          </label>
        </div>
        {errors.where && (
          <span role="alert" className="text-theme-danger-text mt-1 block text-[11px]">
            {errors.where}
          </span>
        )}
      </fieldset>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <TaskField label="Profile" error={errors.profile}>
          <Select
            value={draft.profile}
            invalid={Boolean(errors.profile)}
            onChange={(e) => {
              onChange({ profile: e.target.value });
            }}
          >
            <option value="">Default</option>
            {profiles.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </Select>
        </TaskField>
        <TaskField label="Backend" error={errors.backend}>
          <Select
            value={draft.backend}
            invalid={Boolean(errors.backend)}
            onChange={(e) => {
              onChange({ backend: e.target.value });
            }}
          >
            <option value="">Default</option>
            {BACKENDS.map((b) => (
              <option key={b} value={b}>
                {b}
              </option>
            ))}
          </Select>
        </TaskField>
        <TaskField label="Playbook" error={errors.playbook}>
          <Select
            value={draft.playbook}
            invalid={Boolean(errors.playbook)}
            onChange={(e) => {
              onChange({ playbook: e.target.value });
            }}
          >
            <option value="">Automatic</option>
            <option value="none">None</option>
            {playbooks.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </Select>
        </TaskField>
        <TaskField label="Base branch" error={errors.baseBranch}>
          <Input
            value={draft.baseBranch}
            invalid={Boolean(errors.baseBranch)}
            placeholder="workspace.base_branch"
            onChange={(e) => {
              onChange({ baseBranch: e.target.value });
            }}
          />
        </TaskField>
      </div>
    </div>
  );
}
