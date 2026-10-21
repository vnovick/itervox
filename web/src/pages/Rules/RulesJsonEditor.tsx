// Rules editor JSON view (#157): the layer's file as text, comments and all,
// validated against the rules schema while typing. The save writes the text
// as typed.

import { useId, useState } from 'react';
import { Button } from '../../components/ui/button';
import { parseRulesText } from '../../lib/rulesText';
import { textareaCls } from '../Settings/formStyles';

interface RulesJsonEditorProps {
  initialText: string;
  schemaUrl: string;
  saving: boolean;
  onSave: (raw: string) => void;
}

export function RulesJsonEditor({ initialText, schemaUrl, saving, onSave }: RulesJsonEditorProps) {
  const id = useId();
  const [text, setText] = useState(initialText);
  const result = parseRulesText(text);

  return (
    <div className="space-y-2">
      <label htmlFor={id} className="sr-only">
        Rules file (JSONC)
      </label>
      <textarea
        id={id}
        className={`${textareaCls} min-h-[320px]`}
        spellCheck={false}
        value={text}
        onChange={(e) => {
          setText(e.target.value);
        }}
      />
      <p
        role="status"
        data-testid="rules-json-status"
        className={result.ok ? 'text-theme-success-text text-xs' : 'text-theme-danger-text text-xs'}
      >
        {result.ok
          ? `Valid: ${String(result.rules.length)} rule${result.rules.length === 1 ? '' : 's'}.`
          : result.error}
      </p>
      <p className="text-theme-muted text-[11px]">
        JSONC: comments and trailing commas are kept as written. Schema:{' '}
        <a className="underline" href={schemaUrl} target="_blank" rel="noreferrer">
          {schemaUrl}
        </a>
      </p>
      <Button
        variant="primary"
        size="sm"
        disabled={saving || !result.ok || text === initialText}
        onClick={() => {
          onSave(text);
        }}
      >
        {saving ? 'Saving…' : 'Save file'}
      </Button>
    </div>
  );
}
