export type IssueDetailTab = 'details' | 'changes';

const TABS: readonly { id: IssueDetailTab; label: string }[] = [
  { id: 'details', label: 'Details' },
  { id: 'changes', label: 'Changes' },
];

/** #215 — the issue detail's tab bar (Details | Changes). */
export function IssueDetailTabs({
  tab,
  onChange,
}: {
  tab: IssueDetailTab;
  onChange: (tab: IssueDetailTab) => void;
}) {
  return (
    <div
      role="tablist"
      aria-label="Issue detail"
      className="border-theme-line flex gap-1 border-b px-5"
    >
      {TABS.map((t) => (
        <button
          key={t.id}
          type="button"
          role="tab"
          id={`issue-tab-${t.id}`}
          aria-selected={tab === t.id}
          onClick={() => {
            onChange(t.id);
          }}
          className={`-mb-px border-b-2 px-3 py-2 text-xs font-medium ${
            tab === t.id
              ? 'border-theme-accent text-theme-text'
              : 'text-theme-muted border-transparent hover:opacity-80'
          }`}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}
