// Rules editor form view (#157): edit one layer's rules field by field. The
// save sends the whole list; the daemon writes only what changed, so the
// file's comments stay.

import { useState } from 'react';
import { Button } from '../../components/ui/button';
import type { Rule } from '../../types/schemas';
import { ruleErrors, ruleRow, type RuleRow } from './rulesModel';
import { RuleFields } from './RuleFields';

interface RulesFormProps {
  rules: readonly Rule[];
  saving: boolean;
  onSave: (rules: Rule[]) => void;
}

export function RulesForm({ rules, saving, onSave }: RulesFormProps) {
  const [rows, setRows] = useState<RuleRow[]>(() => rules.map(ruleRow));
  const draft = rows.map((r) => r.rule);
  const errors = ruleErrors(draft);

  const update = (key: string, rule: Rule) => {
    setRows((prev) => prev.map((r) => (r.key === key ? { ...r, rule } : r)));
  };

  return (
    <div className="space-y-3">
      {rows.length === 0 && (
        <p className="text-theme-muted text-sm">This layer has no rules yet.</p>
      )}
      {rows.map((row, i) => (
        <RuleFields
          key={row.key}
          rule={row.rule}
          index={i}
          error={errors?.[i]}
          onChange={(rule) => {
            update(row.key, rule);
          }}
          onRemove={() => {
            setRows((prev) => prev.filter((r) => r.key !== row.key));
          }}
        />
      ))}
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          onClick={() => {
            setRows((prev) => [...prev, ruleRow({ id: '', severity: 'should', text: '' })]);
          }}
        >
          Add rule
        </Button>
        <Button
          variant="primary"
          size="sm"
          disabled={saving || errors !== null}
          onClick={() => {
            onSave(draft);
          }}
        >
          {saving ? 'Saving…' : 'Save rules'}
        </Button>
      </div>
    </div>
  );
}
