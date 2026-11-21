import { BACKEND_LABELS, type AgentBackend } from '../../../lib/backends';
import { fieldLabelCls, helperTextCls, inputCls } from '../formStyles';
import { MODEL_SERVER_ENV_VARS, MODEL_SERVER_URL_EXAMPLE } from './modelServer';

interface ModelServerFieldProps {
  backend: AgentBackend;
  value: string;
  error?: string;
  onChange: (value: string) => void;
}

/**
 * The profile editor's Model server field (agent.profiles.<name>.model_server).
 * Saved through the regular profile save; the daemon checks it again and sets
 * the backend's variable when it runs the agent. No probe here: `itervox
 * doctor` and the desktop app check that the server answers.
 */
export function ModelServerField({ backend, value, error, onChange }: ModelServerFieldProps) {
  const envVar = MODEL_SERVER_ENV_VARS[backend];
  const set = value.trim() !== '';
  return (
    <div>
      <label htmlFor="profile-model-server-input" className={fieldLabelCls}>
        Model server
      </label>
      <input
        id="profile-model-server-input"
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
        }}
        placeholder={`${MODEL_SERVER_URL_EXAMPLE} or $LOCAL_MODEL_URL`}
        aria-invalid={error ? true : undefined}
        aria-describedby="profile-model-server-help"
        className={`${inputCls} font-mono text-xs`}
      />
      <p id="profile-model-server-help" className={helperTextCls}>
        {envVar ? (
          <>
            Optional. A local or self-hosted model server for this profile: Itervox sets{' '}
            <span className="font-mono">{envVar}</span> when it runs the agent.
            {set &&
              ' Its runs get their own quota pool and longer timeouts; check it with `itervox doctor`.'}
          </>
        ) : (
          `${BACKEND_LABELS[backend]} reads its model server from its own config, not from this field.`
        )}
      </p>
      {error && (
        <p role="alert" className="text-theme-danger-text mt-1 text-xs">
          {error}
        </p>
      )}
    </div>
  );
}
