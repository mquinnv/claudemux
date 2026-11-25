import { describe, expect, it } from 'vitest';
import { changedRegions, gestureFor } from './device';

describe('gestureFor', () => {
  it('maps SDK event names to gestures', () => {
    expect(gestureFor('SCROLL_BOTTOM_EVENT')).toBe('down');
    expect(gestureFor('SCROLL_TOP_EVENT')).toBe('up');
    expect(gestureFor('DOUBLE_CLICK_EVENT')).toBe('double');
    expect(gestureFor('CLICK_EVENT')).toBe('tap');
  });
  it('treats a missing event type as a tap', () => {
    expect(gestureFor(undefined)).toBe('tap');
  });
  it('ignores long press and anything else', () => {
    expect(gestureFor('LONG_PRESS_EVENT')).toBeNull();
    expect(gestureFor('FOREGROUND_ENTER_EVENT')).toBeNull();
  });
});

describe('changedRegions', () => {
  const a = { header: 'h', body: 'b', options: 'o' };
  it('returns nothing when nothing changed', () => {
    expect(changedRegions(a, { ...a })).toEqual([]);
  });
  it('returns only the regions whose text changed', () => {
    expect(changedRegions(a, { ...a, body: 'b2' })).toEqual([{ id: 2, name: 'body', content: 'b2' }]);
    expect(changedRegions(a, { header: 'h2', body: 'b', options: 'o2' }).map((r) => r.name)).toEqual(['hdr', 'opts']);
  });
  it('returns all regions when there is no previous draw', () => {
    expect(changedRegions(null, a).map((r) => r.id)).toEqual([1, 2, 3]);
  });
});
