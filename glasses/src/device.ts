import type { Gesture } from './types';

// Ring event name (OsEventTypeList key) to gesture. CLICK can arrive with no
// event type at all on the device, so a missing type is a tap. Long press and
// everything else is ignored.
export function gestureFor(eventType: string | undefined): Gesture | null {
  switch (eventType) {
    case undefined:
    case 'CLICK_EVENT':
      return 'tap';
    case 'SCROLL_BOTTOM_EVENT':
      return 'down';
    case 'SCROLL_TOP_EVENT':
      return 'up';
    case 'DOUBLE_CLICK_EVENT':
      return 'double';
    default:
      return null;
  }
}

export type Drawn = { header: string; body: string; options: string };
export type Region = { id: number; name: string; content: string };

const REGIONS: { id: number; name: string; key: keyof Drawn }[] = [
  { id: 1, name: 'hdr', key: 'header' },
  { id: 2, name: 'body', key: 'body' },
  { id: 3, name: 'opts', key: 'options' },
];

// The regions whose text differs from what is on the glasses now, so each
// update touches only what changed.
export function changedRegions(prev: Drawn | null, next: Drawn): Region[] {
  return REGIONS.filter((r) => prev === null || prev[r.key] !== next[r.key]).map((r) => ({
    id: r.id,
    name: r.name,
    content: next[r.key],
  }));
}
