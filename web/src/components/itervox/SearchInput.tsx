import { Search, X } from 'lucide-react';

interface SearchInputProps {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  label: string;
  className?: string;
  inputClassName?: string;
  clearLabel?: string;
  testId?: string;
}

export function SearchInput({
  value,
  onChange,
  placeholder,
  label,
  className = '',
  inputClassName = '',
  clearLabel = 'Clear search',
  testId,
}: SearchInputProps) {
  return (
    <div className={`relative min-w-0 ${className}`}>
      <Search
        aria-hidden="true"
        className="text-theme-muted pointer-events-none absolute top-1/2 left-3.5 z-10 h-3.5 w-3.5 -translate-y-1/2"
      />
      <input
        type="search"
        aria-label={label}
        data-testid={testId}
        placeholder={placeholder}
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
        }}
        className={`glass-chip text-theme-text placeholder:text-theme-muted focus-visible:ring-theme-accent h-9 w-full py-1.5 pr-8 pl-9 text-[13px] focus:outline-none focus-visible:ring-2 ${inputClassName}`}
      />
      {value.trim() !== '' && (
        <button
          type="button"
          aria-label={clearLabel}
          onClick={() => {
            onChange('');
          }}
          className="text-theme-muted hover:text-theme-text absolute top-1/2 right-2.5 flex h-5 w-5 -translate-y-1/2 items-center justify-center rounded-full transition-colors"
        >
          <X aria-hidden="true" className="h-3.5 w-3.5" />
        </button>
      )}
    </div>
  );
}
