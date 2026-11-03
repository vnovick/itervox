// JSON view of one settings layer (#158): the file as text, comments and all,
// checked against the settings schema while typing. The save writes the text
// as typed to the layer the tab names.

import { useId, useState } from 'react';
import { Button } from '../../../components/ui/button';
import {
  parseSettingsText,
  settingsLayerLabel,
  type SettingsFieldDef,
} from '../../../lib/settingsSchema';
import { textareaCls } from '../formStyles';

interface SettingsLayerJsonProps {
  layerName: string;
  initialText: string;
  schemaUrl: string;
  fields: SettingsFieldDef[];
  saving: boolean;
  onSave: (raw: string) => void;
}

export function SettingsLayerJson({
  layerName,
  initialText,
  schemaUrl,
  fields,
  saving,
  onSave,
}: SettingsLayerJsonProps) {
  const id = useId();
  const [text, setText] = useState(initialText);
  const result = parseSettingsText(text, fields);

  return (
    <div className="space-y-2">
      <label htmlFor={id} className="sr-only">
        Settings file (JSONC)
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
        data-testid="settings-json-status"
        className={result.ok ? 'text-theme-success-text text-xs' : 'text-theme-danger-text text-xs'}
      >
        {result.ok
          ? `Valid: ${String(result.keys)} setting${result.keys === 1 ? '' : 's'}.`
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
        {saving ? 'Saving…' : `Save file to ${settingsLayerLabel(layerName)}`}
      </Button>
    </div>
  );
}
