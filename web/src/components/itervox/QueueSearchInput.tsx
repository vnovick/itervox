import { SearchInput } from './SearchInput';

export function QueueSearchInput({
  value,
  onChange,
  label = 'Search queue',
  placeholder = 'Search queue...',
  testId,
}: {
  value: string;
  onChange: (value: string) => void;
  label?: string;
  placeholder?: string;
  testId?: string;
}) {
  return (
    <SearchInput
      value={value}
      onChange={onChange}
      label={label}
      placeholder={placeholder}
      clearLabel="Clear queue search"
      testId={testId}
      className="w-full"
    />
  );
}
