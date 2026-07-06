// Human-readable subtitles for well-known tracker state names. Rendered in
// the DefaultColumnHeader subtitle slot. Lookup is case + separator insensitive
// so "In Progress", "in_progress", and "in-progress" all match.

const COLUMN_SUBTITLES: Record<string, string> = {
  backlog: 'Unprioritized items',
  'to do': 'Ready to be picked up',
  todo: 'Ready to be picked up',
  'in progress': 'Active work',
  inprogress: 'Active work',
  'in review': 'Awaiting review',
  inreview: 'Awaiting review',
  review: 'Awaiting review',
  done: 'Completed this cycle',
  completed: 'Completed',
  cancelled: 'Closed issues',
  canceled: 'Closed issues',
  duplicate: 'Marked as duplicate',
};

export function columnSubtitle(state: string): string | undefined {
  return COLUMN_SUBTITLES[state.toLowerCase().replace(/[-_]/g, ' ')];
}
