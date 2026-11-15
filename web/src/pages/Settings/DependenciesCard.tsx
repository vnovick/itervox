import { useState, type KeyboardEvent } from 'react';

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

  const current = OPTIONS.find((o) => o.value === mode) ?? OPTIONS[0];
  // Arrow keys move the choice, as in a native radio group.
  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const n = OPTIONS.length;
    const to =
      e.key === 'ArrowRight' || e.key === 'ArrowDown'
        ? (index + 1) % n
        : e.key === 'ArrowLeft' || e.key === 'ArrowUp'
          ? (index - 1 + n) % n
          : -1;
    if (to < 0) return;
    e.preventDefault();
    document.getElementById(`deps-mode-${OPTIONS[to].value}`)?.focus();
    void handleChange(OPTIONS[to].value);
  };

  return (
    <div className="space-y-2">
      {/* The shared segmented control, as a radio group (one of two modes). */}
      <div
        data-testid="deps-analysis-mode"
        role="radiogroup"
        aria-label="Dependency analysis mode"
        aria-describedby="deps-mode-help"
        className="segmented"
      >
        {OPTIONS.map((opt, i) => {
          const checked = mode === opt.value;
          return (
            <button
              key={opt.value}
              id={`deps-mode-${opt.value}`}
              type="button"
              role="radio"
              aria-checked={checked}
              title={opt.description}
              tabIndex={checked ? 0 : -1}
              disabled={saving}
              className="min-h-8 px-4 text-sm disabled:cursor-wait"
              onClick={() => {
                void handleChange(opt.value);
              }}
              onKeyDown={(e) => {
                onKeyDown(e, i);
              }}
            >
              {opt.label}
            </button>
          );
        })}
      </div>
      <p id="deps-mode-help" className="field-help">
        {current.description}
      </p>
      {error && (
        <span role="alert" className="text-theme-danger-text block text-xs">
          {error}
        </span>
      )}
    </div>
  );
}
