import { useId, useMemo, useState } from 'react';

interface TagInputProps {
  chips: string[];
  onChange: (chips: string[]) => void;
  /** Tailwind classes applied to each chip span. */
  chipClassName: string;
  /** Tailwind classes applied to the Add button. */
  addButtonClassName: string;
  /** Placeholder text for the inline add input. */
  placeholder?: string;
  /** Optional suggestion list shown as quick-add pills and input autocomplete. */
  suggestions?: string[];
  suggestionLabel?: string;
}

/**
 * Reusable tag-input: a chip list with an inline text input to add entries
 * and a remove button on each chip.
 */
export function TagInput({
  chips,
  onChange,
  chipClassName,
  addButtonClassName,
  placeholder = '+ Add state',
  suggestions = [],
  suggestionLabel = 'Suggestions',
}: TagInputProps) {
  const [inputValue, setInputValue] = useState('');
  const datalistId = useId();
  const normalizedChips = useMemo(() => chips.map((chip) => chip.toLowerCase()), [chips]);
  const availableSuggestions = useMemo(
    () =>
      suggestions.filter((suggestion, index) => {
        const normalized = suggestion.toLowerCase();
        return (
          suggestion.trim() !== '' &&
          !normalizedChips.includes(normalized) &&
          suggestions.indexOf(suggestion) === index
        );
      }),
    [normalizedChips, suggestions],
  );

  const add = () => {
    const value = inputValue.trim();
    if (value && !normalizedChips.includes(value.toLowerCase())) onChange([...chips, value]);
    setInputValue('');
  };

  const remove = (chip: string) => {
    onChange(chips.filter((c) => c !== chip));
  };

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-2">
        {chips.map((chip) => (
          <span
            key={chip}
            className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium ${chipClassName}`}
          >
            {chip}
            <button
              type="button"
              onClick={() => {
                remove(chip);
              }}
              className="ml-0.5 transition-opacity hover:opacity-60"
              title={`Remove ${chip}`}
            >
              ×
            </button>
          </span>
        ))}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <input
          type="text"
          list={availableSuggestions.length > 0 ? datalistId : undefined}
          value={inputValue}
          onChange={(e) => {
            setInputValue(e.target.value);
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              add();
            }
          }}
          placeholder={placeholder}
          className="min-w-[11rem] flex-1 rounded-[var(--radius-sm)] border border-[var(--line)] bg-[var(--panel-strong)] px-3 py-2 text-xs text-[var(--text)] focus:outline-none"
        />
        {availableSuggestions.length > 0 && (
          <datalist id={datalistId}>
            {availableSuggestions.map((suggestion) => (
              <option key={suggestion} value={suggestion} />
            ))}
          </datalist>
        )}
        <button
          type="button"
          onClick={add}
          disabled={inputValue.trim() === ''}
          className={`rounded-[var(--radius-sm)] px-2.5 py-1.5 text-xs transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${addButtonClassName}`}
        >
          Add
        </button>
      </div>

      {availableSuggestions.length > 0 && (
        <div className="space-y-1">
          <p className="text-theme-muted text-[11px]">{suggestionLabel}</p>
          <div className="flex flex-wrap gap-2">
            {availableSuggestions.slice(0, 12).map((suggestion) => (
              <button
                key={suggestion}
                type="button"
                onClick={() => {
                  onChange([...chips, suggestion]);
                }}
                className="border-theme-line bg-theme-panel text-theme-text-secondary hover:bg-theme-bg-soft rounded-full border px-2 py-0.5 text-[11px] transition-colors"
              >
                {suggestion}
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
