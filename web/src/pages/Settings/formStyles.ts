// Shared field recipes for the settings, rules and memory forms (and the
// profile/automation editors that reuse them). Surfaces come from the
// shared field well (FIELD_CLASS) and the .field-label / .glass-card
// classes in index.css, so every form reads as one system.
import { FIELD_CLASS } from '../../components/ui/Input';

export const fieldLabelCls = 'field-label';

export const inputCls = `${FIELD_CLASS} min-h-10 py-2`;

export const selectCls = `${inputCls} cursor-pointer`;

export const textareaCls = `${FIELD_CLASS} py-2 font-mono text-xs resize-y min-h-[72px]`;

export const helperTextCls = 'text-theme-muted mt-1 text-xs';

export const fieldSurfaceCls = 'glass-card space-y-3 rounded-[18px] p-3.5';

export const checkboxCls =
  'mt-0.5 h-3.5 w-3.5 rounded border-theme-line-strong bg-theme-panel accent-theme-accent';
