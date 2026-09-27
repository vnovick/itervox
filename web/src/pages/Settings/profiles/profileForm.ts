import { z } from 'zod';
import { AllowedAgentActionSchema, type ProfileDef } from '../../../types/schemas';
import { buildCanonicalCommand, draftFromProfileDef } from '../profileCommands';
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
    allowedActions: z.array(AllowedAgentActionSchema),
    createIssueState: z.string(),
    // CORE-047 round 2: read-only raw actions from a newer daemon.
    unknownAllowedActions: z.array(z.string()).optional(),
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
    unknownAllowedActions: def.unknownAllowedActions,
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
