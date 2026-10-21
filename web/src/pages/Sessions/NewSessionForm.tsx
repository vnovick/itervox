import { useId, useState } from 'react';
import { Button } from '../../components/ui/button';
import { Select } from '../../components/ui/Select';
import { Textarea } from '../../components/ui/Textarea';
import { useStartSession, type NewSessionInput } from '../../queries/sessionsTmux';
import type { NewSessionOptions } from '../../types/schemas';

export interface NewSessionFormProps {
  options: NewSessionOptions;
  onClose: () => void;
  /** Called with the new session's id once it started. */
  onStarted: (id: string) => void;
}

const EMPTY_PROFILES: readonly string[] = [];

/**
 * "New session" (#178): starts an interactive Claude Code or Codex CLI in a
 * new tmux session on the daemon's host, in the project folder or one of
 * itervox's workspaces, optionally with a profile and a first prompt.
 */
export function NewSessionForm({ options, onClose, onStarted }: NewSessionFormProps) {
  const [backend, setBackend] = useState<NewSessionInput['backend']>('claude');
  const [cwd, setCwd] = useState(options.projectRoot);
  const [profile, setProfile] = useState('');
  const [prompt, setPrompt] = useState('');
  const start = useStartSession();
  const profiles = options.profiles.length > 0 ? options.profiles : EMPTY_PROFILES;
  const uid = useId();

  return (
    <form
      data-testid="new-session-form"
      aria-label="New session"
      className="bg-theme-panel border-theme-line space-y-3 rounded-lg border p-3"
      onSubmit={(e) => {
        e.preventDefault();
        const input: NewSessionInput = { backend };
        if (cwd && cwd !== options.projectRoot) input.cwd = cwd;
        if (profile) input.profile = profile;
        if (prompt.trim()) input.prompt = prompt.trim();
        start.mutate(input, {
          onSuccess: (row) => {
            onStarted(row.id);
          },
        });
      }}
    >
      <h2 className="text-theme-text text-sm font-semibold">New session in tmux</h2>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="text-theme-text-secondary space-y-1 text-xs">
          <label htmlFor={`${uid}-backend`}>Agent</label>
          <Select
            id={`${uid}-backend`}
            value={backend}
            onChange={(e) => {
              setBackend(e.target.value === 'codex' ? 'codex' : 'claude');
            }}
          >
            <option value="claude">Claude Code</option>
            <option value="codex">Codex</option>
          </Select>
        </div>
        <div className="text-theme-text-secondary space-y-1 text-xs">
          <label htmlFor={`${uid}-cwd`}>Folder</label>
          <Select
            id={`${uid}-cwd`}
            value={cwd}
            onChange={(e) => {
              setCwd(e.target.value);
            }}
          >
            {options.folders.map((f) => (
              <option key={f} value={f}>
                {f === options.projectRoot ? `${f} (project)` : f}
              </option>
            ))}
          </Select>
        </div>
        <div className="text-theme-text-secondary space-y-1 text-xs">
          <label htmlFor={`${uid}-profile`}>Profile (optional)</label>
          <Select
            id={`${uid}-profile`}
            value={profile}
            onChange={(e) => {
              setProfile(e.target.value);
            }}
          >
            <option value="">Default command, confining permissions</option>
            {profiles.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </Select>
        </div>
      </div>
      <div className="text-theme-text-secondary block space-y-1 text-xs">
        <label htmlFor={`${uid}-prompt`}>First prompt (optional)</label>
        <Textarea
          id={`${uid}-prompt`}
          rows={3}
          value={prompt}
          onChange={(e) => {
            setPrompt(e.target.value);
          }}
          placeholder="What should the agent do first?"
        />
      </div>
      <div className="flex items-center gap-2">
        <Button type="submit" variant="primary" size="sm" disabled={start.isPending}>
          {start.isPending ? 'Starting…' : 'Start session'}
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
