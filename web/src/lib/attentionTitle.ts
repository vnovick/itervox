// CORE-078 — the "(N) " attention prefix PageMeta puts on every page title.
// Pure so the format is testable without Helmet.
export const ATTENTION_TITLE_CAP = 99;

export function withAttentionPrefix(title: string, count: number): string {
  if (count <= 0) return title;
  const shown = count > ATTENTION_TITLE_CAP ? `${String(ATTENTION_TITLE_CAP)}+` : String(count);
  return `(${shown}) ${title}`;
}
