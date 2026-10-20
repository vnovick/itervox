// Pixel position of a textarea's caret, for placing the autocomplete popover
// (#87). Uses a hidden mirror element with the textarea's text styles; in an
// environment without layout (tests) it returns the top-left corner.

const COPIED_STYLES = [
  'boxSizing',
  'width',
  'paddingTop',
  'paddingRight',
  'paddingBottom',
  'paddingLeft',
  'borderTopWidth',
  'borderRightWidth',
  'borderBottomWidth',
  'borderLeftWidth',
  'fontFamily',
  'fontSize',
  'fontWeight',
  'fontStyle',
  'letterSpacing',
  'lineHeight',
  'tabSize',
  'textTransform',
  'wordSpacing',
] as const;

export interface CaretPosition {
  top: number;
  left: number;
  height: number;
}

export function caretPosition(textarea: HTMLTextAreaElement, caret: number): CaretPosition {
  const doc = textarea.ownerDocument;
  const style = doc.defaultView?.getComputedStyle(textarea);
  const mirror = doc.createElement('div');
  const ms = mirror.style;
  ms.position = 'absolute';
  ms.visibility = 'hidden';
  ms.whiteSpace = 'pre-wrap';
  ms.overflowWrap = 'break-word';
  ms.top = '0';
  ms.left = '-9999px';
  if (style) {
    for (const key of COPIED_STYLES) ms[key] = style[key];
  }
  mirror.textContent = textarea.value.slice(0, caret);
  const marker = doc.createElement('span');
  marker.textContent = textarea.value.slice(caret) || '.';
  mirror.appendChild(marker);
  doc.body.appendChild(mirror);
  const lineHeight = parseFloat(style?.lineHeight ?? '') || 20;
  const pos = {
    top: marker.offsetTop - textarea.scrollTop,
    left: marker.offsetLeft - textarea.scrollLeft,
    height: lineHeight,
  };
  doc.body.removeChild(mirror);
  return pos;
}
