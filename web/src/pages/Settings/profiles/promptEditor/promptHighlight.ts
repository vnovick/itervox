// Highlighting for the prompt editor (#122): Markdown (CommonMark + GFM)
// through @lezer/markdown, and Liquid `{{ … }}` objects and `{% … %}` tags
// as marks on top of it. Liquid is not a separate grammar: a prompt is
// Markdown with Liquid inside, so the marks layer over whatever Markdown
// node they sit in (a heading, a list item, inline code).
//
// The classes are stable names (cm-md-*, cm-liquid-*) so the theme can colour
// them from the dashboard's tokens and tests can find them.

import { Language, defineLanguageFacet, syntaxHighlighting } from '@codemirror/language';
import type { Extension } from '@codemirror/state';
import {
  Decoration,
  MatchDecorator,
  ViewPlugin,
  type DecorationSet,
  type EditorView,
  type ViewUpdate,
} from '@codemirror/view';
import { tagHighlighter, tags } from '@lezer/highlight';
import { GFM, parser as markdownParser } from '@lezer/markdown';

// The parser straight from @lezer/markdown (it carries its own highlight
// tags). @codemirror/lang-markdown would also pull in the HTML, CSS and
// JavaScript languages for embedded blocks, tripling the editor chunk.
const promptMarkdown = new Language(
  defineLanguageFacet({ commentTokens: { block: { open: '<!--', close: '-->' } } }),
  markdownParser.configure(GFM),
  [],
  'markdown',
);

/** Markdown token classes. */
export const markdownHighlighter = tagHighlighter([
  { tag: tags.heading, class: 'cm-md-heading' },
  { tag: tags.monospace, class: 'cm-md-code' },
  { tag: tags.emphasis, class: 'cm-md-emphasis' },
  { tag: tags.strong, class: 'cm-md-strong' },
  { tag: [tags.link, tags.url], class: 'cm-md-link' },
  { tag: tags.quote, class: 'cm-md-quote' },
  { tag: [tags.processingInstruction, tags.contentSeparator], class: 'cm-md-mark' },
  { tag: tags.list, class: 'cm-md-list' },
]);

const liquidObject = Decoration.mark({ class: 'cm-liquid-object' });
const liquidTag = Decoration.mark({ class: 'cm-liquid-tag' });

/** `{{ … }}` objects and `{% … %}` tags, one line at a time. */
export const LIQUID_RE = /\{\{.*?\}\}|\{%.*?%\}/g;

const liquidMatcher = new MatchDecorator({
  regexp: LIQUID_RE,
  decoration: (match) => (match[0].startsWith('{{') ? liquidObject : liquidTag),
});

/** Marks Liquid objects and tags. */
export const liquidHighlight = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = liquidMatcher.createDeco(view);
    }
    update(update: ViewUpdate) {
      this.decorations = liquidMatcher.updateDeco(update, this.decorations);
    }
  },
  { decorations: (plugin) => plugin.decorations },
);

/** Markdown + Liquid highlighting. */
export function promptHighlight(): Extension {
  return [promptMarkdown, syntaxHighlighting(markdownHighlighter), liquidHighlight];
}
