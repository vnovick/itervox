import { useForm, useWatch } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { useId } from 'react';
import { Modal } from '../../components/ui/modal';
import { Button } from '../../components/ui/button';
import { fieldLabelCls as labelCls, inputCls } from './formStyles';

// ─── Zod schema — mirrors Go backend validation in config/validate.go ────────

export const sshHostSchema = z.object({
  host: z
    .string()
    .min(1, 'Host address is required.')
    .regex(/^\S+$/, 'Must not contain spaces.')
    .refine((h) => !h.startsWith('-'), 'Must not start with a dash.'),
  description: z.string(),
});

export type SSHHostFormValues = z.infer<typeof sshHostSchema>;

// ─── Component ───────────────────────────────────────────────────────────────

interface AddSSHHostModalProps {
  isOpen: boolean;
  onClose: () => void;
  onAdd: (host: string, description: string) => Promise<boolean>;
}

export function AddSSHHostModal({ isOpen, onClose, onAdd }: AddSSHHostModalProps) {
  const headingId = useId();
  const {
    register,
    handleSubmit,
    control,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<SSHHostFormValues>({
    resolver: zodResolver(sshHostSchema),
    defaultValues: { host: '', description: '' },
  });

  // useWatch subscribes without the `watch()` function wrapper so the
  // component is compatible with React Compiler memoization (the `watch()`
  // form trips the react-hooks/incompatible-library lint rule).
  const hostValue = useWatch({ control, name: 'host' });

  const onSubmit = handleSubmit(async (values) => {
    const ok = await onAdd(values.host.trim(), values.description.trim());
    if (ok) {
      reset();
      onClose();
    }
  });

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      showCloseButton
      padded
      className="max-w-md"
      ariaLabelledBy={headingId}
    >
      <h2 id={headingId} className="text-theme-text mb-4 text-xl font-bold tracking-tight">
        Add Worker Host
      </h2>

      {/* Host type selector */}
      <div className="mb-5 flex gap-2">
        <div
          className="choice-card flex-1 flex-col items-start gap-0.5 rounded-[14px]"
          data-active="true"
        >
          <div className="text-theme-text text-[15px] font-semibold">SSH</div>
          <div className="text-theme-text-secondary text-xs">Remote host via SSH</div>
        </div>
        <button
          type="button"
          disabled
          className="choice-card flex-1 flex-col items-start gap-0.5 rounded-[14px]"
          title="Coming in a future release"
        >
          <div className="flex items-center gap-1.5">
            <span className="text-theme-text text-[15px] font-semibold">Docker</span>
            <span className="pill pill-neutral pill-sm">Soon</span>
          </div>
          <div className="text-theme-text-secondary text-xs">Ephemeral containers</div>
        </button>
      </div>

      <form
        onSubmit={(e) => {
          void onSubmit(e);
        }}
        className="space-y-4"
      >
        <div>
          <label htmlFor="ssh-host-address" className={labelCls}>
            Host address <span className="text-theme-danger-text">*</span>
          </label>
          <input
            id="ssh-host-address"
            className={inputCls}
            type="text"
            placeholder="build-server.example.com or 192.168.1.10:22"
            // eslint-disable-next-line jsx-a11y/no-autofocus -- dialog opens on its primary field; useDialogLayer captures the opener before this runs, so focus still returns to it on close (M5-close)
            autoFocus
            {...register('host')}
          />
          {errors.host && (
            <p role="alert" className="text-theme-danger-text mt-1 text-xs">
              {errors.host.message}
            </p>
          )}
          <p className="text-theme-text-secondary mt-1.5 text-xs">
            Use <code className="font-mono">host</code> or{' '}
            <code className="font-mono">host:port</code>. Defaults to port 22.
          </p>
        </div>

        <div>
          <label htmlFor="ssh-host-description" className={labelCls}>
            Description (optional)
          </label>
          <input
            id="ssh-host-description"
            className={inputCls}
            type="text"
            placeholder="e.g. Build server — 32 cores, 64 GB RAM"
            {...register('description')}
          />
        </div>

        {/* Host key warning */}
        <div className="bg-theme-warning-soft text-theme-text space-y-1.5 rounded-[var(--radius-md)] px-3.5 py-3 text-[13px] leading-relaxed">
          <div className="text-theme-warning-text flex items-center gap-1.5 font-semibold">
            <span aria-hidden="true">⚠</span> SSH host key required
          </div>
          <p>
            The host's key must be in <code className="font-mono">~/.ssh/known_hosts</code> on this
            machine before Itervox can connect. Run once to pre-accept it:
          </p>
          <pre className="font-mono text-xs break-all whitespace-pre-wrap select-all">
            {`ssh-keyscan -H ${hostValue.trim() || '<host>'} >> ~/.ssh/known_hosts`}
          </pre>
        </div>

        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" size="lg" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" size="lg" disabled={isSubmitting}>
            {isSubmitting ? 'Adding…' : 'Add host'}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
