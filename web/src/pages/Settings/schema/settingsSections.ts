// Section titles and anchors of the schema-driven settings form (#158).

const SECTION_TITLES: Record<string, string> = {
  agent: 'Agent',
  polling: 'Polling',
  server: 'Server',
  workspace: 'Workspace',
};

/** The display title of a settings-schema section. */
export function settingsSectionTitle(section: string): string {
  return SECTION_TITLES[section] ?? section.charAt(0).toUpperCase() + section.slice(1);
}

/** The DOM id of a section's disclosure, for the toolbar's section links. */
export function settingsSectionId(section: string): string {
  return `settings-section-${section}`;
}
