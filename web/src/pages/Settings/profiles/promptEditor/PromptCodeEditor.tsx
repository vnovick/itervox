import { useEffect, useMemo, useRef, useState } from 'react';
import { completionStatus, currentCompletions } from '@codemirror/autocomplete';
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { search, searchKeymap } from '@codemirror/search';
import {
  Annotation,
  Compartment,
  EditorState,
  Transaction,
  type Extension,
} from '@codemirror/state';
import { EditorView, keymap, placeholder as placeholderExt } from '@codemirror/view';
import { useSkillsInventory } from '../../../../queries/skills';
import { referenceCompletions, type Completion } from '../promptCompletions';
import {
  defaultSuggestionProvider,
  promptAutocomplete,
  type PromptSuggestionProvider,
} from './promptAutocomplete';
import { promptHighlight } from './promptHighlight';
import { promptTheme } from './promptTheme';

export interface PromptCodeEditorProps {
  value: string;
  onChange: (value: string) => void;
  /** Accessible name of the editor; the suggestion list is "<label> suggestions". */
  label: string;
  placeholder?: string;
  /** Liquid variables offered after `{{`. */
  variables: readonly Completion[];
  /** Replaces the default suggestion provider (inventory + variables + filters). */
  suggestions?: PromptSuggestionProvider;
}

/** Marks a change that came from the value prop, not from the user. */
const fromProp = Annotation.define<boolean>();

function labelExtensions(label: string, placeholder: string | undefined): Extension {
  return [
    EditorView.contentAttributes.of({ 'aria-label': label }),
    EditorState.phrases.of({ Completions: `${label} suggestions` }),
    placeholder ? placeholderExt(placeholder) : [],
  ];
}

function suggestionStatus(state: EditorState): string {
  if (completionStatus(state) !== 'active') return '';
  const n = currentCompletions(state).length;
  return `${String(n)} suggestion${n === 1 ? '' : 's'}; arrow keys to choose, Enter to insert, Escape to close`;
}

/**
 * The CodeMirror 6 prompt editor (#122): Markdown and Liquid highlighting,
 * line wrapping, undo history, search (Mod-F), and the #87 suggestions on
 * @codemirror/autocomplete. Loaded lazily by MarkdownPromptEditor so
 * CodeMirror stays out of the main bundle.
 */
export default function PromptCodeEditor({
  value,
  onChange,
  label,
  placeholder,
  variables,
  suggestions,
}: PromptCodeEditorProps) {
  const host = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const labelConf = useRef(new Compartment());
  const initialValue = useRef(value);
  const onChangeRef = useRef(onChange);
  const [status, setStatus] = useState('');

  const { data: inventory } = useSkillsInventory();
  const references = useMemo(() => referenceCompletions(inventory), [inventory]);
  const provider = useMemo(
    () => suggestions ?? defaultSuggestionProvider(references, variables),
    [suggestions, references, variables],
  );
  const providerRef = useRef(provider);

  useEffect(() => {
    onChangeRef.current = onChange;
    providerRef.current = provider;
  }, [onChange, provider]);

  useEffect(() => {
    if (!host.current) return;
    const view = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: initialValue.current,
        extensions: [
          labelConf.current.of([]),
          history(),
          search({ top: true }),
          EditorView.lineWrapping,
          promptHighlight(),
          promptAutocomplete(() => providerRef.current),
          keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap]),
          promptTheme,
          EditorView.updateListener.of((update) => {
            const edited = update.transactions.some(
              (tr) => tr.docChanged && !tr.annotation(fromProp),
            );
            if (edited) onChangeRef.current(update.state.doc.toString());
            setStatus(suggestionStatus(update.state));
          }),
        ],
      }),
    });
    viewRef.current = view;
    return () => {
      view.destroy();
      viewRef.current = null;
    };
  }, []);

  useEffect(() => {
    viewRef.current?.dispatch({
      effects: labelConf.current.reconfigure(labelExtensions(label, placeholder)),
    });
  }, [label, placeholder]);

  // A value set from outside (a reset, a suggested profile) replaces the text.
  useEffect(() => {
    const view = viewRef.current;
    if (!view) return;
    const current = view.state.doc.toString();
    if (current !== value) {
      view.dispatch({
        changes: { from: 0, to: current.length, insert: value },
        annotations: [fromProp.of(true), Transaction.addToHistory.of(false)],
      });
    }
  }, [value]);

  return (
    <div className="relative">
      <div ref={host} data-testid="prompt-code-editor" />
      <span role="status" aria-live="polite" className="sr-only">
        {status}
      </span>
    </div>
  );
}
