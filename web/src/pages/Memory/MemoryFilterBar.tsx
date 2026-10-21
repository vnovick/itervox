// Memory page filters (#202): search plus type, scope, profile, tag and
// usage, kept in uiStore across navigation.

import { useId, type ReactNode } from 'react';
import { useUIStore, type MemoryUsageFilter } from '../../store/uiStore';
import {
  MEMORY_SCOPES,
  MEMORY_TYPES,
  type MemoryScope,
  type MemoryType,
} from '../../types/schemas';
import { fieldLabelCls, inputCls, selectCls } from '../Settings/formStyles';
import { SCOPE_INFO, TYPE_LABELS } from './memoryModel';

const USAGE: { id: MemoryUsageFilter; label: string }[] = [
  { id: 'all', label: 'Any usage' },
  { id: 'used', label: 'Used by a run' },
  { id: 'unused', label: 'Never used' },
];

export function MemoryFilterBar({ tags, profiles }: { tags: string[]; profiles: string[] }) {
  const uid = useId();
  const f = useUIStore((s) => s.memoryFilters);
  const set = useUIStore((s) => s.setMemoryFilters);
  return (
    <div className="grid gap-3 sm:grid-cols-3 lg:grid-cols-6" role="search">
      <div className="sm:col-span-3 lg:col-span-6">
        <label className={fieldLabelCls} htmlFor={`${uid}-q`}>
          Search
        </label>
        <input
          id={`${uid}-q`}
          className={inputCls}
          type="search"
          placeholder="Text, id, tag or issue"
          value={f.search}
          onChange={(e) => {
            set({ search: e.target.value });
          }}
        />
      </div>
      <Filter
        label="Type"
        uid={`${uid}-type`}
        value={f.type}
        onChange={(v) => {
          set({ type: v as MemoryType | 'all' });
        }}
      >
        <option value="all">All types</option>
        {MEMORY_TYPES.map((t) => (
          <option key={t} value={t}>
            {TYPE_LABELS[t]}
          </option>
        ))}
      </Filter>
      <Filter
        label="Scope"
        uid={`${uid}-scope`}
        value={f.scope}
        onChange={(v) => {
          set({ scope: v as MemoryScope | 'all' });
        }}
      >
        <option value="all">All scopes</option>
        {MEMORY_SCOPES.map((s) => (
          <option key={s} value={s}>
            {SCOPE_INFO[s].label}
          </option>
        ))}
      </Filter>
      <Filter
        label="Profile"
        uid={`${uid}-profile`}
        value={f.profile}
        onChange={(v) => {
          set({ profile: v });
        }}
      >
        <option value="">Any profile</option>
        {profiles.map((p) => (
          <option key={p} value={p}>
            {p}
          </option>
        ))}
      </Filter>
      <Filter
        label="Tag"
        uid={`${uid}-tag`}
        value={f.tag}
        onChange={(v) => {
          set({ tag: v });
        }}
      >
        <option value="">Any tag</option>
        {tags.map((t) => (
          <option key={t} value={t}>
            {t}
          </option>
        ))}
      </Filter>
      <Filter
        label="Usage"
        uid={`${uid}-usage`}
        value={f.usage}
        onChange={(v) => {
          set({ usage: v as MemoryUsageFilter });
        }}
      >
        {USAGE.map((u) => (
          <option key={u.id} value={u.id}>
            {u.label}
          </option>
        ))}
      </Filter>
    </div>
  );
}

function Filter({
  label,
  uid,
  value,
  onChange,
  children,
}: {
  label: string;
  uid: string;
  value: string;
  onChange: (value: string) => void;
  children: ReactNode;
}) {
  return (
    <div>
      <label className={fieldLabelCls} htmlFor={uid}>
        {label}
      </label>
      <select
        id={uid}
        className={selectCls}
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
        }}
      >
        {children}
      </select>
    </div>
  );
}
