import { useMemo } from 'react';
import { TagInput } from '../../../components/itervox/TagInput';
import { useProjects } from '../../../queries/projects';
import { useTrackerLabels, useTrackerTeams, useTrackerUsers } from '../../../queries/trackerScope';
import { useItervoxStore } from '../../../store/itervoxStore';
import type { FilterGroup } from '../../../types/schemas';
import { PRIORITY_LABELS, type FilterConditionKey } from '../../../utils/filterGroups';

// #198 — the value picker of one filter condition. Each list condition is a
// chip list fed by the tracker's choices (GET /api/v1/projects and
// /api/v1/tracker/{teams,labels,users}); a value outside the choices can be
// typed in.

const EMPTY: string[] = [];
const CHIP = 'bg-[var(--accent-soft)] text-[var(--accent-strong)]';
const ADD = 'bg-[var(--accent-soft)] text-[var(--accent-strong)] hover:opacity-80';
const FIELD =
  'border-theme-line bg-theme-bg text-theme-text block w-full rounded-[var(--radius-sm)] border px-2 py-1.5 text-sm';

type Value = FilterGroup[FilterConditionKey];

interface InputProps {
  conditionKey: FilterConditionKey;
  value: Value;
  onChange: (value: Value) => void;
  inputId: string;
}

function ChipPicker({
  chips,
  onChange,
  suggestions,
  inputId,
}: {
  chips: string[];
  onChange: (v: string[]) => void;
  suggestions: string[];
  inputId: string;
}) {
  return (
    <TagInput
      id={inputId}
      chips={chips}
      onChange={onChange}
      suggestions={suggestions}
      suggestionLabel="Choices"
      placeholder="Add a value"
      chipClassName={CHIP}
      addButtonClassName={ADD}
    />
  );
}

function ProjectsPicker(props: {
  chips: string[];
  onChange: (v: string[]) => void;
  inputId: string;
}) {
  const { data: projects = [] } = useProjects();
  const suggestions = useMemo(() => [...projects.map((p) => p.slug), '__no_project__'], [projects]);
  return <ChipPicker {...props} suggestions={suggestions} />;
}

function TeamsPicker(props: { chips: string[]; onChange: (v: string[]) => void; inputId: string }) {
  const { data: teams = [] } = useTrackerTeams();
  const suggestions = useMemo(() => teams.map((t) => t.key), [teams]);
  return <ChipPicker {...props} suggestions={suggestions} />;
}

function LabelsPicker(props: {
  chips: string[];
  onChange: (v: string[]) => void;
  inputId: string;
}) {
  const { data: labels = EMPTY } = useTrackerLabels();
  return <ChipPicker {...props} suggestions={labels} />;
}

function StatesPicker(props: {
  chips: string[];
  onChange: (v: string[]) => void;
  inputId: string;
}) {
  const active = useItervoxStore((s) => s.snapshot?.activeStates ?? EMPTY);
  return <ChipPicker {...props} suggestions={active} />;
}

function AssigneePicker({
  value,
  onChange,
  inputId,
}: {
  value: string;
  onChange: (v: string) => void;
  inputId: string;
}) {
  const { data: users = [] } = useTrackerUsers();
  const known = value === '' || value === 'me' || users.some((u) => u.id === value);
  return (
    <select
      id={inputId}
      value={value}
      onChange={(e) => {
        onChange(e.target.value);
      }}
      className={FIELD}
    >
      <option value="">Pick a user…</option>
      <option value="me">Me (the API key&apos;s user)</option>
      {!known && <option value={value}>{value}</option>}
      {users.map((u) => (
        <option key={u.id} value={u.id}>
          {u.name || u.email || u.id}
        </option>
      ))}
    </select>
  );
}

function PriorityPicker({ value, onChange }: { value: number[]; onChange: (v: number[]) => void }) {
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1">
      {PRIORITY_LABELS.map((label, p) => (
        <label key={label} className="flex cursor-pointer items-center gap-1.5 text-sm">
          <input
            type="checkbox"
            checked={value.includes(p)}
            onChange={() => {
              onChange(value.includes(p) ? value.filter((v) => v !== p) : [...value, p].sort());
            }}
            className="h-4 w-4 rounded"
            style={{ accentColor: 'var(--accent)' }}
          />
          <span className="text-theme-text">
            P{p} {label}
          </span>
        </label>
      ))}
    </div>
  );
}

function TextValue({
  value,
  onChange,
  inputId,
  placeholder,
  list,
}: {
  value: string;
  onChange: (v: string) => void;
  inputId: string;
  placeholder: string;
  list?: string;
}) {
  return (
    <input
      id={inputId}
      type="text"
      value={value}
      list={list}
      placeholder={placeholder}
      onChange={(e) => {
        onChange(e.target.value);
      }}
      className={FIELD}
    />
  );
}

/** The picker for one condition's value. */
export function FilterConditionInput({ conditionKey, value, onChange, inputId }: InputProps) {
  const chips = Array.isArray(value) ? (value as string[]) : EMPTY;
  const text = typeof value === 'string' ? value : '';
  const setList = (v: string[]) => {
    onChange(v);
  };
  switch (conditionKey) {
    case 'projects':
      return <ProjectsPicker chips={chips} onChange={setList} inputId={inputId} />;
    case 'teams':
      return <TeamsPicker chips={chips} onChange={setList} inputId={inputId} />;
    case 'labels_any':
    case 'labels_all':
    case 'labels_none':
      return <LabelsPicker chips={chips} onChange={setList} inputId={inputId} />;
    case 'states':
      return <StatesPicker chips={chips} onChange={setList} inputId={inputId} />;
    case 'assignee':
      return <AssigneePicker value={text} onChange={onChange} inputId={inputId} />;
    case 'priority':
      return (
        <PriorityPicker
          value={Array.isArray(value) ? (value as number[]) : []}
          onChange={onChange}
        />
      );
    case 'cycle':
      return (
        <>
          <TextValue
            value={text}
            onChange={onChange}
            inputId={inputId}
            placeholder="current"
            list="filter-cycle-choices"
          />
          <datalist id="filter-cycle-choices">
            <option value="current" />
          </datalist>
        </>
      );
    case 'milestone':
      return (
        <TextValue
          value={text}
          onChange={onChange}
          inputId={inputId}
          placeholder="Milestone name"
        />
      );
  }
}
