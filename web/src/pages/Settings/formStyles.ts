// Shared form recipes for settings, profile, automation, rule and memory
// forms. Surfaces and colours come from index.css (`.field-label`,
// `.field-help`, `.field-group`) and the Input primitive's FIELD_CLASS, so
// every form in the app reads the same.
import { FIELD_CLASS } from '../../components/ui/Input';

export const fieldLabelCls = 'field-label';

export const inputCls = `${FIELD_CLASS} min-h-10 py-2`;

export const selectCls = `${inputCls} cursor-pointer`;

export const textareaCls = `${FIELD_CLASS} min-h-[72px] resize-y py-2.5 font-mono text-xs leading-relaxed`;

export const helperTextCls = 'field-help mt-1';

export const fieldSurfaceCls = 'field-group space-y-2';

export const checkboxCls = 'accent-theme-accent mt-0.5 h-4 w-4 shrink-0 rounded';
