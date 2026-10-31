import { Input } from '../../ui/Input';

/** #251 — one input per `{{var}}` placeholder in the title and instructions. */
export function TaskTemplateVars({
  names,
  values,
  error,
  onChange,
}: {
  names: readonly string[];
  values: Readonly<Record<string, string>>;
  error?: string;
  onChange: (name: string, value: string) => void;
}) {
  if (names.length === 0) return null;
  return (
    <fieldset data-testid="new-task-vars">
      <legend className="text-theme-text-secondary mb-1 text-xs font-medium">
        Template values
      </legend>
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {names.map((name) => (
          <label key={name} className="flex items-center gap-2 text-[13px]">
            <code className="text-theme-muted w-24 shrink-0 truncate">{name}</code>
            <Input
              aria-label={`Value for ${name}`}
              value={values[name] ?? ''}
              invalid={Boolean(error) && (values[name] ?? '').trim() === ''}
              onChange={(e) => {
                onChange(name, e.target.value);
              }}
            />
          </label>
        ))}
      </div>
      {error && (
        <span role="alert" className="text-theme-danger-text mt-1 block text-[11px]">
          {error}
        </span>
      )}
    </fieldset>
  );
}
