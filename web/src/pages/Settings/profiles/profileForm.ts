import { z } from 'zod';
import type { ProfileDef } from '../../../types/schemas';
import {
  buildCanonicalCommand,
  draftFromProfileDef,
  type AllowedAgentAction,
  type SupportedBackend,
} from '../profileCommands';
import type { SuggestedProfile } from './suggestedProfiles';

export const profileFormSchema = z
  .object({
    name: z
      .string()
      .min(1, 'Profile name is required.')
      .regex(/^\S+$/, 'Profile name must not contain spaces.'),
    enabled: z.boolean(),
    backend: z.enum(['claude', 'codex']),
    model: z.string(),
    command: z.string().min(1, 'Command is required.'),
    prompt: z.string(),
    soul: z.string(),
    instructions: z.string(),
    soulFile: z.string(),
    instructionsFile: z.string(),
    allowedActions: z.array(
      z.enum(['comment', 'comment_pr', 'create_issue', 'move_state', 'provide_input']),
    ),
    createIssueState: z.string(),
  })
  .superRefine((values, ctx) => {
    if (values.allowedActions.includes('create_issue') && values.createIssueState.trim() === '') {
      ctx.addIssue({
        code: 'custom',
        path: ['createIssueState'],
        message: 'Choose the tracker column/state for follow-up issues.',
      });
    }
  });

export type ProfileFormValues = z.infer<typeof profileFormSchema>;

export function emptyProfileValues(): ProfileFormValues {
  return {
    name: '',
    enabled: true,
    backend: 'claude',
    model: '',
    command: buildCanonicalCommand('claude', ''),
    prompt: '',
    soul: '',
    instructions: '',
    soulFile: '',
    instructionsFile: '',
    allowedActions: [],
    createIssueState: '',
  };
}

export function profileValuesFromDef(name: string, def: ProfileDef): ProfileFormValues {
  const draft = draftFromProfileDef(def);
  return {
    name,
    enabled: draft.enabled,
    backend: draft.backend,
    model: draft.model,
    command: draft.command,
    prompt: draft.prompt,
    soul: draft.soul,
    instructions: draft.instructions,
    soulFile: draft.soulFile,
    instructionsFile: draft.instructionsFile,
    allowedActions: draft.allowedActions,
    createIssueState: draft.createIssueState,
  };
}

export function profileValuesFromSuggestion(suggestion: SuggestedProfile): ProfileFormValues {
  return {
    name: suggestion.id,
    enabled: true,
    backend: suggestion.backend,
    model: suggestion.model,
    command: buildCanonicalCommand(suggestion.backend, suggestion.model),
    prompt: suggestion.prompt,
    soul: '',
    instructions: suggestion.prompt,
    soulFile: '',
    instructionsFile: '',
    allowedActions: suggestion.allowedActions,
    createIssueState: suggestion.createIssueState ?? '',
  };
}

export function profileValuesWithName(
  name: string,
  enabled: boolean,
  backend: SupportedBackend,
  model: string,
  command: string,
  prompt: string,
  soul: string,
  instructions: string,
  allowedActions: AllowedAgentAction[],
  createIssueState: string,
): ProfileFormValues {
  return {
    name,
    enabled,
    backend,
    model,
    command,
    prompt,
    soul,
    instructions,
    soulFile: '',
    instructionsFile: '',
    allowedActions,
    createIssueState,
  };
}
