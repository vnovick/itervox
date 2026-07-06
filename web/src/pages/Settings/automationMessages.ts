// Named status messages shown by AutomationsCard. Kept in their own module so
// AutomationsCard.tsx is a pure component-only module and remains eligible
// for Vite fast-refresh (mixing component and non-component exports
// disqualifies the module — see web/src/pages/Settings/profiles/profileBadges.ts
// for the same pattern extracted from ProfileEditorFields.tsx).

export const MSG_AUTOMATIONS_DUPLICATE_ID = 'Automation IDs must be unique.';

export const MSG_AUTOMATIONS_SAVE_SUCCESS =
  'Saved to WORKFLOW.md. The daemon will reload automations shortly.';

export function MSG_AUTOMATIONS_TEMPLATE_REQUIRES_PROFILE(label: string, profile: string): string {
  return `Template "${label}" requires the "${profile}" profile.`;
}
