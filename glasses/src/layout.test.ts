import { describe, expect, it } from 'vitest';
import { COLS, fit, wrap } from './layout';

describe('wrap', () => {
  it('breaks at word boundaries within cols', () => {
    expect(wrap('aaa bbb ccc ddd', 7, 5)).toBe('aaa bbb\nccc ddd');
  });

  it('cuts at a word and adds an ellipsis', () => {
    const out = wrap('aaa bbb ccc ddd eee', 7, 2);
    expect(out).toBe('aaa bbb\nccc…');
    for (const line of out.split('\n')) expect(line.length).toBeLessThanOrEqual(7);
  });

  it('does not add an ellipsis when everything fits', () => {
    expect(wrap('aaa bbb', 7, 1)).toBe('aaa bbb');
  });

  it('hard-splits a word longer than cols', () => {
    expect(wrap('abcdefghij', 4, 5)).toBe('abcd\nefgh\nij');
  });

  it('keeps explicit newlines as line breaks', () => {
    expect(wrap('one\ntwo', 10, 5)).toBe('one\ntwo');
  });

  it('returns an empty string for empty text', () => {
    expect(wrap('', 10, 3)).toBe('');
  });
});

describe('fit', () => {
  it('pads left so right is flush', () => {
    expect(fit('abc', '1/2', 10)).toBe('abc    1/2');
  });

  it('truncates left with an ellipsis when too long', () => {
    const out = fit('abcdefghijkl', '1/2', 10);
    expect(out.length).toBe(10);
    expect(out.endsWith(' 1/2')).toBe(true);
    expect(out).toContain('…');
  });

  it('uses COLS-wide output when given COLS', () => {
    expect(fit('x', 'y', COLS).length).toBe(COLS);
  });
});
