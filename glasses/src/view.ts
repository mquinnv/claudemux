import { COLS, fit, wrap } from './layout';
import type { Effect, Gesture, NextResponse, Row, State } from './types';

const STALE_MS = 10_000;
const WINDOW = 5;
const BODY_LINES = 3;
const CONFIRM_TEXT = 'tap again to confirm';

// Rows the cursor moves over: on the session screen the options, then defer
// and skip; on the fleet screen the sessions.
export function rows(s: State): Row[] {
  if (s.screen === 'fleet') {
    return (s.fleet?.sessions ?? []).map((f) => ({ id: f.name, label: `${f.name}  ${f.title}` }));
  }
  const d = s.next?.detail;
  if (!d) return [];
  const opts: Row[] = d.options.map((o) => {
    const r: Row = { id: o.id, label: o.label };
    if (o.broad) r.broad = true;
    if (o.suggested) r.suggested = true;
    return r;
  });
  return [...opts, { id: 'defer', label: 'defer' }, { id: 'skip', label: 'skip' }];
}

function clampCursor(cursor: number, n: number): number {
  return Math.max(0, Math.min(cursor, n - 1));
}

export function reduce(s: State, g: Gesture): { state: State; effect: Effect } {
  const list = rows(s);

  // While the header says the lobby is unreachable no rows are shown, so
  // nothing but screen switching may act on them.
  if (s.error && g !== 'double') return { state: s, effect: null };

  if (g === 'up' || g === 'down') {
    const cursor = clampCursor(s.cursor + (g === 'down' ? 1 : -1), list.length);
    return { state: { ...s, cursor, confirming: null }, effect: null };
  }

  if (g === 'double') {
    if (s.screen === 'session') {
      return { state: { ...s, screen: 'fleet', cursor: 0, confirming: null }, effect: { kind: 'loadFleet' } };
    }
    return { state: { ...s, screen: 'session', cursor: 0, confirming: null }, effect: null };
  }

  const row = list[s.cursor];
  if (!row) return { state: s, effect: null };

  if (s.screen === 'fleet') {
    return {
      state: { ...s, screen: 'session', cursor: 0, confirming: null },
      effect: { kind: 'open', session: row.id },
    };
  }

  const d = s.next?.detail;
  if (!d) return { state: s, effect: null };
  const cleared = { ...s, confirming: null };
  if (row.id === 'defer') return { state: cleared, effect: { kind: 'defer', session: d.session } };
  if (row.id === 'skip') return { state: cleared, effect: { kind: 'skip', session: d.session } };
  if (row.broad && s.confirming !== row.id) {
    return { state: { ...s, confirming: row.id }, effect: null };
  }
  return {
    state: cleared,
    effect: { kind: 'answer', session: d.session, episode: d.episode, option: row.id },
  };
}

// Folds a fresh /remote/next response in. A different session or episode is
// a different question, so the cursor and any pending confirm reset.
export function applyNext(s: State, n: NextResponse, now: number): State {
  const prev = s.next?.detail;
  const cur = n.detail;
  const same = prev && cur && prev.session === cur.session && prev.episode === cur.episode;
  const merged: State = { ...s, next: n, lastOkAt: now, error: null, flash: null };
  if (!same) return { ...merged, cursor: 0, confirming: null };
  return { ...merged, cursor: clampCursor(s.cursor, rows(merged).length) };
}

// Shows text in place of the title until the next successful poll.
export function withFlash(s: State, text: string | null): State {
  return { ...s, flash: text };
}

function optionWindow(list: Row[], cursor: number, confirming: string | null): string {
  const start = Math.max(0, Math.min(cursor - 2, list.length - WINDOW));
  return list
    .slice(start, start + WINDOW)
    .map((r, i) => {
      const label = r.id === confirming ? CONFIRM_TEXT : r.label;
      const mark = start + i === cursor ? '> ' : '  ';
      return mark + label + (r.suggested ? ' *' : '');
    })
    .join('\n');
}

export function render(s: State, now: number): { header: string; body: string; options: string } {
  if (s.error) {
    return { header: 'lobby not reachable', body: '', options: '' };
  }

  if (s.screen === 'fleet') {
    const total = s.fleet?.sessions.length ?? 0;
    return {
      header: fit('sessions', String(total), COLS),
      body: '',
      options: optionWindow(rows(s), s.cursor, null),
    };
  }

  const d = s.next?.detail;
  if (!s.next || !d) {
    return { header: 'all clear', body: `${s.next?.working ?? 0} working`, options: '' };
  }

  const age = now - s.lastOkAt;
  const { pos, total } = s.next.queue;
  const right = age > STALE_MS ? `${Math.floor(age / 1000)}s ago` : `${pos > 0 ? pos : '-'}/${total}`;
  const body = [wrap(d.body, COLS, BODY_LINES), d.note ?? ''].filter((p) => p !== '').join('\n');
  return {
    header: fit(s.flash ?? d.title, right, COLS),
    body,
    options: optionWindow(rows(s), s.cursor, s.confirming),
  };
}
