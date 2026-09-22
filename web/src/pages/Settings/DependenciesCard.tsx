import { useState } from 'react';

type DepsAnalysisMode = 'auto' | 'manual';

interface Option {
  value: DepsAnalysisMode;
  label: string;
  description: string;
}

const OPTIONS: Option[] = [
  {
    value: 'auto',
    label: 'Auto',
    description:
      "Run the analyzer automatically on the scheduler's debounce and minimum-interval rules.",
  },
  {
    value: 'manual',
    label: 'Manual',
    description:
      'Only when you click Analyze dependencies or call the API. The blocker audit still runs on poll.',
  },
];

interface DependenciesCardProps {
  mode: DepsAnalysisMode;
  onSetMode: (mode: DepsAnalysisMode) => Promise<boolean>;
}

export function DependenciesCard({ mode, onSetMode }: DependenciesCardProps) {
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const handleChange = async (next: DepsAnalysisMode) => {
    if (saving || next === mode) return;
    setSaving(true);
    setError('');
    const ok = await onSetMode(next);
    setSaving(false);
    if (!ok) setError('Failed to update dependency analysis mode. Please try again.');
  };

  return (
    <div className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-md)] border">
      <div className="border-theme-line bg-theme-panel-strong border-b px-5 py-4">
        <h2 className="text-theme-text text-sm font-semibold">Dependencies</h2>
      </div>
      <div className="px-5 py-5">
        <div
          data-testid="deps-analysis-mode"
          role="radiogroup"
          aria-label="Dependency analysis mode"
          className="space-y-3"
        >
          {OPTIONS.map((opt) => (
            <label key={opt.value} className="flex cursor-pointer items-start gap-3">
              <span
                aria-hidden="true"
                className="border-theme-line relative mt-0.5 flex h-4 w-4 flex-shrink-0 items-center justify-center rounded-full border"
              >
                {mode === opt.value && <span className="bg-theme-accent h-2 w-2 rounded-full" />}
              </span>
              <input
                type="radio"
                name="deps-analysis-mode"
                className="sr-only"
                aria-label={opt.label}
                checked={mode === opt.value}
                disabled={saving}
                onChange={() => {
                  void handleChange(opt.value);
                }}
              />
              <div>
                <span className="text-theme-text block text-sm font-medium">{opt.label}</span>
                <span className="text-theme-text-secondary mt-0.5 block text-xs">
                  {opt.description}
                </span>
              </div>
            </label>
          ))}
        </div>
        {error && (
          <span role="alert" className="text-theme-danger mt-2 block text-xs">
            {error}
          </span>
        )}
      </div>
    </div>
  );
}
