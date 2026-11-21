export type ChangesView = 'files' | 'lanes';

const VIEWS: readonly { id: ChangesView; label: string }[] = [
  { id: 'files', label: 'Files' },
  { id: 'lanes', label: 'Lanes' },
];

/** The Changes tab's Files | Lanes switch (guardrails.lanes). */
export function ChangesViewToggle({
  view,
  onChange,
}: {
  view: ChangesView;
  onChange: (view: ChangesView) => void;
}) {
  return (
    <div role="group" aria-label="Changes view" className="segmented text-xs">
      {VIEWS.map((v) => (
        <button
          key={v.id}
          type="button"
          aria-pressed={view === v.id}
          onClick={() => {
            onChange(v.id);
          }}
          className="px-2.5 py-0.5"
        >
          {v.label}
        </button>
      ))}
    </div>
  );
}
