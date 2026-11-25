// Width of the glasses text display in characters. Provisional: tune on the
// device. Every width calculation in the app must use this constant, and the
// lobby's remoteBodyCols (cmd/claudemux-head/remotedetail.go) must match it.
export const COLS = 46;

const ELLIPSIS = '…';

function splitWord(word: string, cols: number): string[] {
  const parts: string[] = [];
  for (let i = 0; i < word.length; i += cols) parts.push(word.slice(i, i + cols));
  return parts;
}

function wrapParagraph(par: string, cols: number): string[] {
  const words = par.split(/\s+/).filter((w) => w.length > 0);
  if (words.length === 0) return [''];
  const lines: string[] = [];
  let cur = '';
  for (const word of words) {
    if (cur !== '' && cur.length + 1 + word.length <= cols) {
      cur += ' ' + word;
      continue;
    }
    if (cur !== '') lines.push(cur);
    if (word.length <= cols) {
      cur = word;
    } else {
      const parts = splitWord(word, cols);
      cur = parts.pop() ?? '';
      lines.push(...parts);
    }
  }
  if (cur !== '') lines.push(cur);
  return lines;
}

// Word-wraps text to at most maxLines lines of at most cols characters. When
// text is cut, the last line ends with an ellipsis, dropping whole words
// where it can.
export function wrap(text: string, cols: number, maxLines: number): string {
  if (text.trim() === '' || maxLines <= 0) return '';
  const lines = text.split('\n').flatMap((p) => wrapParagraph(p, cols));
  if (lines.length <= maxLines) return lines.join('\n');
  const kept = lines.slice(0, maxLines);
  let last = kept[maxLines - 1] ?? '';
  while (last.length + ELLIPSIS.length > cols && last.includes(' ')) {
    last = last.slice(0, last.lastIndexOf(' '));
  }
  if (last.length + ELLIPSIS.length > cols) last = last.slice(0, Math.max(0, cols - ELLIPSIS.length));
  kept[maxLines - 1] = last + ELLIPSIS;
  return kept.join('\n');
}

// Like wrap, but keeps the LAST maxLines lines: for text whose end matters
// most, such as a session's last message. When text is cut, the first line
// starts with an ellipsis, dropping whole words where it can.
export function wrapTail(text: string, cols: number, maxLines: number): string {
  if (text.trim() === '' || maxLines <= 0) return '';
  const lines = text.split('\n').flatMap((p) => wrapParagraph(p, cols));
  if (lines.length <= maxLines) return lines.join('\n');
  const kept = lines.slice(lines.length - maxLines);
  let first = kept[0] ?? '';
  while (first.length + ELLIPSIS.length > cols && first.includes(' ')) {
    first = first.slice(first.indexOf(' ') + 1);
  }
  if (first.length + ELLIPSIS.length > cols) first = first.slice(first.length - Math.max(0, cols - ELLIPSIS.length));
  kept[0] = ELLIPSIS + first;
  return kept.join('\n');
}

// Left text padded so right ends flush at the cols-th column. Left is cut
// with an ellipsis when the two do not fit together.
export function fit(left: string, right: string, cols: number): string {
  const room = cols - right.length - 1;
  if (left.length > room) left = room > 0 ? left.slice(0, room - 1) + ELLIPSIS : '';
  return left + ' '.repeat(Math.max(1, cols - left.length - right.length)) + right;
}
