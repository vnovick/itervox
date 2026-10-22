// Memory page, Lessons tab (#217): promote an approved lesson to a rule in
// the user or project rules layer. The rule links back to the lesson
// (id lesson-<entry id>, description naming it) and the lesson is archived
// with a pointer to the rule, so runs get it as a rule from then on.

import { useId, useState } from 'react';
import { Button } from '../../components/ui/button';
import { usePromoteLessonToRule } from '../../queries/memory';
import type { MemoryEntry } from '../../types/schemas';
import { fieldLabelCls, selectCls } from '../Settings/formStyles';

const LAYERS = [
  { value: 'project', label: 'Project (.itervox/rules.json, committed)' },
  { value: 'user', label: 'User (~/.itervox/rules.json)' },
] as const;

type Layer = (typeof LAYERS)[number]['value'];
type Severity = 'must' | 'should';

export function LessonPromoteRule({ entry }: { entry: MemoryEntry }) {
  const uid = useId();
  const promote = usePromoteLessonToRule();
  const [layer, setLayer] = useState<Layer>('project');
  const [severity, setSeverity] = useState<Severity>('should');

  return (
    <div className="mt-3 flex flex-wrap items-end gap-2" data-testid={`promote-rule-${entry.id}`}>
      <div>
        <label className={fieldLabelCls} htmlFor={`${uid}-layer`}>
          Rules layer
        </label>
        <select
          id={`${uid}-layer`}
          className={selectCls}
          value={layer}
          onChange={(e) => {
            setLayer(e.target.value as Layer);
          }}
        >
          {LAYERS.map((l) => (
            <option key={l.value} value={l.value}>
              {l.label}
            </option>
          ))}
        </select>
      </div>
      <div>
        <label className={fieldLabelCls} htmlFor={`${uid}-severity`}>
          Severity
        </label>
        <select
          id={`${uid}-severity`}
          className={selectCls}
          value={severity}
          onChange={(e) => {
            setSeverity(e.target.value as Severity);
          }}
        >
          <option value="should">should</option>
          <option value="must">must</option>
        </select>
      </div>
      <Button
        size="sm"
        variant="secondary"
        disabled={promote.isPending}
        onClick={() => {
          promote.mutate({ id: entry.id, layer, severity });
        }}
      >
        Promote to rule
      </Button>
    </div>
  );
}
