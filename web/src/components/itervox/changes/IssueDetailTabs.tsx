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
    <div role="tablist" aria-label="Issue detail" className="segmented mx-6 mb-1 self-start">
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
          className="min-h-8 px-4 py-1 text-sm"
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}
