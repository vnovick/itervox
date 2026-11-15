// Command palette actions. Each is offered only when it can run: the caller
// passes null (or false) for what is unavailable — no capability, not the
// scope for it, nothing to act on — and the action is left out.

import type { Command } from './paletteModel';

export interface PaletteActionContext {
  /** Opens the New task dialog (operator scope); null hides it. */
  newTask: (() => void) | null;
  /** The dispatch pause (capability + operator scope); null hides it. */
  dispatch: { paused: boolean; set: (paused: boolean) => void } | null;
  theme: { current: 'light' | 'dark'; toggle: () => void } | null;
  /**
   * Hub projects to switch to: the others the switcher lists, empty unless
   * it lists several.
   */
  projects: readonly string[];
  switchProject: (name: string) => void;
  /** Runs the dependency analyzer; null when it cannot run now. */
  analyzeDeps: (() => void) | null;
  /** Refreshes the model lists (admin); null hides it. */
  refreshModels: (() => void) | null;
  /** The issue in view and its PR, when the diff named one. */
  pr: { identifier: string; open: () => void } | null;
  /** The issue in view's draft review, when there is one to send. */
  review: { identifier: string; comments: number; send: () => void } | null;
}

export function actionCommands(ctx: PaletteActionContext): Command[] {
  const out: Command[] = [];
  if (ctx.newTask) {
    const run = ctx.newTask;
    out.push({ id: 'action:new-task', group: 'actions', label: 'New task', top: true, run });
  }
  if (ctx.dispatch) {
    const { paused, set } = ctx.dispatch;
    out.push(
      paused
        ? {
            id: 'action:resume-dispatch',
            group: 'actions',
            label: 'Resume dispatch',
            top: true,
            run: () => {
              set(false);
            },
            confirm: {
              prompt:
                'Resume dispatch? Waiting issues, retries, reviews and automations start again as slots allow.',
              confirmLabel: 'Resume dispatch',
              tone: 'primary',
            },
          }
        : {
            id: 'action:pause-dispatch',
            group: 'actions',
            label: 'Pause dispatch',
            danger: true,
            top: true,
            run: () => {
              set(true);
            },
            confirm: {
              prompt:
                'Pause all dispatch? Running agents continue. Nothing new starts — no dispatch, retries, reviewers, automations or input resumes — until you resume.',
              confirmLabel: 'Pause dispatch',
            },
          },
    );
  }
  if (ctx.pr) {
    const { identifier, open } = ctx.pr;
    out.push({
      id: 'action:open-pr',
      group: 'actions',
      label: `Open ${identifier}’s pull request`,
      keywords: 'PR github',
      top: true,
      run: open,
    });
  }
  if (ctx.review) {
    const { identifier, comments, send } = ctx.review;
    const what = `${String(comments)} ${comments === 1 ? 'comment' : 'comments'}`;
    out.push({
      id: 'action:send-review',
      group: 'actions',
      label: `Send review of ${identifier} (${what})`,
      keywords: 'draft agent continue',
      top: true,
      run: send,
      confirm: {
        prompt: `Send the draft review of ${identifier} (${what} and the note) to the agent?`,
        confirmLabel: 'Send review',
        tone: 'primary',
      },
    });
  }
  if (ctx.theme) {
    const { current, toggle } = ctx.theme;
    out.push({
      id: 'action:theme',
      group: 'actions',
      label: `Switch to ${current === 'dark' ? 'light' : 'dark'} theme`,
      keywords: 'appearance dark light mode',
      top: true,
      run: toggle,
    });
  }
  if (ctx.analyzeDeps) {
    out.push({
      id: 'action:analyze-deps',
      group: 'actions',
      label: 'Analyze dependencies',
      keywords: 'deps blockers graph',
      run: ctx.analyzeDeps,
    });
  }
  if (ctx.refreshModels) {
    out.push({
      id: 'action:refresh-models',
      group: 'actions',
      label: 'Refresh models',
      keywords: 'available models backends',
      run: ctx.refreshModels,
    });
  }
  for (const name of ctx.projects) {
    out.push({
      id: `action:project:${name}`,
      group: 'actions',
      label: `Switch to project ${name}`,
      run: () => {
        ctx.switchProject(name);
      },
    });
  }
  return out;
}
