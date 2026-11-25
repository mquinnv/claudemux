import { getAdvW, getTextWidth } from '@evenrealities/pretext';

// Text layout in pixels, measured with the G2 firmware's own font metrics
// (@evenrealities/pretext). The font is proportional: a space is 5 px, a W
// 16 px, so nothing here counts characters.

// Width every line of text is laid out to: the 576 px display less a safety
// margin, so the firmware never re-wraps a line. Every width calculation in
// the app uses this constant. The lobby's remoteBodyCols x remoteBodyLines
// (46 x 3, cmd/claudemux-head/remotedetail.go) is only a character-count
// approximation of this layout.
export const WIDTH_PX = 560;

// The firmware font has the one-character ellipsis (10 px).
export const ELLIPSIS = '…';

export type Wrapped = { lines: string[]; cut: boolean };

const width = getTextWidth;

// Variation selectors only pick an emoji's presentation; the font has no
// glyph for them, and they mean nothing on their own.
const isVariationSelector = (cp: number) => cp === 0xfe0e || cp === 0xfe0f;

// Replacement for a character the firmware font cannot draw (it would show
// a placeholder box), or undefined to keep it.
function replacement(ch: string): string | undefined {
  if (ch === '\n' || ch === ' ') return undefined;
  if (/\s/.test(ch)) return ' ';
  if (ch === '`') return "'";
  const cp = ch.codePointAt(0) ?? 0;
  if (isVariationSelector(cp)) return '';
  return getAdvW(cp) > 0 ? undefined : '?';
}

// Text the firmware font can draw: a backtick becomes an apostrophe, any other
// character the font lacks becomes "?", whitespace other than newlines becomes
// a space, and emoji variation selectors are dropped.
export function clean(text: string): string {
  let out = '';
  for (const ch of text) out += replacement(ch) ?? ch;
  return out;
}

// Whether clean shows anything other than the text itself: a visible
// character replaced, not just whitespace normalised.
export function replacesGlyphs(text: string): boolean {
  for (const ch of text) {
    const r = replacement(ch);
    if (r !== undefined && r !== ' ' && r !== '') return true;
  }
  return false;
}

// Longest prefix of chars (whole code points) for which fits holds; at least
// one code point when force is set, so a hard split always makes progress.
function longestPrefix(chars: string[], fits: (s: string) => boolean, force = false): number {
  let n = 0;
  while (n < chars.length && fits(chars.slice(0, n + 1).join(''))) n++;
  return force ? Math.max(1, n) : n;
}

function wrapParagraph(par: string, maxPx: number): string[] {
  const words = par.split(/\s+/).filter((w) => w.length > 0);
  if (words.length === 0) return [''];
  const fits = (s: string) => width(s) <= maxPx;
  const lines: string[] = [];
  let cur = '';
  for (const word of words) {
    if (cur !== '' && fits(cur + ' ' + word)) {
      cur += ' ' + word;
      continue;
    }
    if (cur !== '') lines.push(cur);
    // A word wider than a line is split where the line is full.
    let rest = [...word];
    while (!fits(rest.join(''))) {
      const n = longestPrefix(rest, fits, true);
      lines.push(rest.slice(0, n).join(''));
      rest = rest.slice(n);
    }
    cur = rest.join('');
  }
  if (cur !== '') lines.push(cur);
  return lines;
}

function allLines(text: string, maxPx: number): string[] {
  return text.split('\n').flatMap((p) => wrapParagraph(p, maxPx));
}

// Word-wraps text to at most maxLines lines of at most maxPx pixels. When
// text is cut, the last line ends with an ellipsis, dropping whole words
// where it can.
export function wrap(text: string, maxPx: number, maxLines: number): Wrapped {
  if (text.trim() === '' || maxLines <= 0) return { lines: [], cut: text.trim() !== '' };
  const lines = allLines(text, maxPx);
  if (lines.length <= maxLines) return { lines, cut: false };
  const kept = lines.slice(0, maxLines);
  let last = kept[maxLines - 1] ?? '';
  const fits = (s: string) => width(s + ELLIPSIS) <= maxPx;
  while (!fits(last) && last.includes(' ')) last = last.slice(0, last.lastIndexOf(' '));
  if (!fits(last)) last = [...last].slice(0, longestPrefix([...last], fits)).join('');
  kept[maxLines - 1] = last + ELLIPSIS;
  return { lines: kept, cut: true };
}

// Like wrap, but keeps the LAST maxLines lines: for text whose end matters
// most, such as a session's last message. When text is cut, the first line
// starts with an ellipsis, dropping whole words where it can.
export function wrapTail(text: string, maxPx: number, maxLines: number): Wrapped {
  if (text.trim() === '' || maxLines <= 0) return { lines: [], cut: text.trim() !== '' };
  const lines = allLines(text, maxPx);
  if (lines.length <= maxLines) return { lines, cut: false };
  const kept = lines.slice(lines.length - maxLines);
  let first = kept[0] ?? '';
  const fits = (s: string) => width(ELLIPSIS + s) <= maxPx;
  while (!fits(first) && first.includes(' ')) first = first.slice(first.indexOf(' ') + 1);
  if (!fits(first)) {
    const chars = [...first].reverse();
    const n = longestPrefix(chars, (s) => fits([...s].reverse().join('')));
    first = chars.slice(0, n).reverse().join('');
  }
  kept[0] = ELLIPSIS + first;
  return { lines: kept, cut: true };
}

// One line: text followed by tail, at most maxPx pixels. When it does not
// fit, text is cut with an ellipsis and tail is kept.
export function truncate(text: string, maxPx: number, tail = ''): string {
  if (width(text + tail) <= maxPx) return text + tail;
  const fits = (s: string) => width(s.trimEnd() + ELLIPSIS + tail) <= maxPx;
  const chars = [...text];
  return chars.slice(0, longestPrefix(chars, fits)).join('').trimEnd() + ELLIPSIS + tail;
}

// One line: left, then right placed as near the right edge of maxPx as
// whole spaces allow. Left is cut with an ellipsis when the two do not fit
// together with a space between them.
export function fit(left: string, right: string, maxPx: number): string {
  const l = truncate(left, maxPx, ' ' + right).slice(0, -(right.length + 1));
  let pad = ' ';
  while (width(l + pad + ' ' + right) <= maxPx) pad += ' ';
  return l + pad + right;
}
