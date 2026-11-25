import { getAdvW, getTextWidth } from '@evenrealities/pretext';
import { describe, expect, it } from 'vitest';
import { ELLIPSIS, WIDTH_PX, clean, fit, replacesGlyphs, truncate, wrap, wrapTail } from './layout';

const w = getTextWidth;
const allFit = (lines: string[], max: number) => lines.every((l) => w(l) <= max);

describe('wrap', () => {
  it('breaks at spaces within the pixel width', () => {
    const max = w('aaa bbb');
    expect(wrap('aaa bbb ccc ddd', max, 5)).toEqual({ lines: ['aaa bbb', 'ccc ddd'], cut: false });
  });

  it('cuts at a word, ends with an ellipsis and reports the cut', () => {
    const max = w('aaa bbb');
    const out = wrap('aaa bbb ccc ddd eee', max, 2);
    expect(out.cut).toBe(true);
    expect(out.lines).toEqual(['aaa bbb', `ccc${ELLIPSIS}`]);
    expect(allFit(out.lines, max)).toBe(true);
  });

  it('does not report a cut when everything fits', () => {
    expect(wrap('aaa bbb', WIDTH_PX, 1)).toEqual({ lines: ['aaa bbb'], cut: false });
  });

  it('splits a line of 46 wide characters so every line fits', () => {
    const text = 'W'.repeat(46);
    expect(w(text)).toBeGreaterThan(WIDTH_PX);
    const out = wrap(text, WIDTH_PX, 3);
    expect(out.lines.length).toBe(2);
    expect(out.lines.join('')).toBe(text);
    expect(allFit(out.lines, WIDTH_PX)).toBe(true);
    expect(out.cut).toBe(false);
  });

  it('fills a line of typical text to the pixel width, not 46 characters', () => {
    const text = 'the quick brown fox jumps over the lazy dog '.repeat(4).trim();
    const out = wrap(text, WIDTH_PX, 5);
    expect(out.lines[0]!.length).toBeGreaterThan(46);
    expect(allFit(out.lines, WIDTH_PX)).toBe(true);
  });

  it('keeps explicit newlines as line breaks', () => {
    expect(wrap('one\ntwo', WIDTH_PX, 5).lines).toEqual(['one', 'two']);
  });

  it('returns no lines for empty text', () => {
    expect(wrap('  ', WIDTH_PX, 3)).toEqual({ lines: [], cut: false });
  });
});

describe('wrapTail', () => {
  it('keeps the last lines and marks the cut at the start', () => {
    const max = w('aaa bbb');
    const out = wrapTail('aaa bbb ccc ddd eee fff', max, 2);
    expect(out.cut).toBe(true);
    expect(out.lines[1]).toBe('eee fff');
    expect(out.lines[0]!.startsWith(ELLIPSIS)).toBe(true);
    expect(out.lines[0]!.endsWith('ddd')).toBe(true);
    expect(allFit(out.lines, max)).toBe(true);
  });

  it('adds nothing when everything fits', () => {
    expect(wrapTail('aaa bbb', WIDTH_PX, 2)).toEqual({ lines: ['aaa bbb'], cut: false });
  });

  it('cuts wide characters to fit with the ellipsis', () => {
    const out = wrapTail('M'.repeat(200), WIDTH_PX, 2);
    expect(out.lines.length).toBe(2);
    expect(allFit(out.lines, WIDTH_PX)).toBe(true);
  });
});

describe('truncate', () => {
  it('returns text that fits unchanged, with its tail', () => {
    expect(truncate('> Yes', WIDTH_PX, ' *')).toBe('> Yes *');
  });

  it('cuts by pixels with an ellipsis and keeps the tail', () => {
    const out = truncate('> ' + 'Wm'.repeat(40), WIDTH_PX, ' *');
    expect(w(out)).toBeLessThanOrEqual(WIDTH_PX);
    expect(out.endsWith(`${ELLIPSIS} *`)).toBe(true);
    expect(out.startsWith('> Wm')).toBe(true);
  });
});

describe('fit', () => {
  it('puts the right part within a space of the right edge', () => {
    for (const left of ['abc', 'x', 'a medium title of some sort']) {
      const out = fit(left, '1/2', WIDTH_PX);
      expect(out.startsWith(left)).toBe(true);
      expect(out.endsWith(' 1/2')).toBe(true);
      expect(w(out)).toBeLessThanOrEqual(WIDTH_PX);
      expect(w(out)).toBeGreaterThan(WIDTH_PX - 10);
    }
  });

  it('truncates a long left with an ellipsis and keeps the right part at the edge', () => {
    const out = fit('Implement the WWWWWW pixel layout for the glasses remote and much more '.repeat(3), '12s ago', WIDTH_PX);
    expect(out).toContain(ELLIPSIS);
    expect(out.endsWith(' 12s ago')).toBe(true);
    expect(w(out)).toBeLessThanOrEqual(WIDTH_PX);
    expect(w(out)).toBeGreaterThan(WIDTH_PX - 10);
  });
});

describe('clean', () => {
  it('replaces a backtick with an apostrophe', () => {
    expect(clean('run `ls`')).toBe("run 'ls'");
  });

  it('replaces characters the font lacks with ?', () => {
    expect(getAdvW(0xe000)).toBe(0);
    expect(clean('ab')).toBe('a?b');
  });

  it('keeps newlines, the ellipsis and characters the font has', () => {
    expect(getAdvW(ELLIPSIS.codePointAt(0)!)).toBeGreaterThan(0);
    expect(clean(`one\ntwo ${ELLIPSIS} é`)).toBe(`one\ntwo ${ELLIPSIS} é`);
  });

  it('turns other whitespace into spaces', () => {
    expect(clean('a\tb\r\nc')).toBe('a b \nc');
  });

  it('drops emoji variation selectors', () => {
    expect(clean('ok ❤️')).toBe('ok ❤');
  });

  it('replacesGlyphs says whether anything visible was substituted', () => {
    expect(replacesGlyphs('a `b`')).toBe(true);
    expect(replacesGlyphs('a')).toBe(true);
    expect(replacesGlyphs('plain\ttext\n')).toBe(false);
  });
});
