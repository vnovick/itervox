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
    <div>
      <div>
        <div
          data-testid="deps-analysis-mode"
          role="radiogroup"
          aria-label="Dependency analysis mode"
          className="grid gap-2 sm:grid-cols-2"
        >
          {OPTIONS.map((opt) => (
            <label key={opt.value} className="choice-card cursor-pointer items-start">
              <input
                type="radio"
                name="deps-analysis-mode"
                className="accent-theme-accent mt-0.5 shrink-0"
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
          <span role="alert" className="text-theme-danger-text mt-2 block text-xs">
            {error}
          </span>
        )}
      </div>
    </div>
  );
}
