import { describe, expect, it } from 'vitest';
import { COLS } from './layout';
import type { FleetResponse, NextResponse, RemoteDetail, RemoteOption, State } from './types';
import { applyNext, reduce, render, rows, withFlash } from './view';

const opt = (id: string, label: string, extra: Partial<RemoteOption> = {}): RemoteOption => ({
  id,
  label,
  ...extra,
});

function detail(over: Partial<RemoteDetail> = {}): RemoteDetail {
  return {
    session: 'alpha',
    title: 'Fix the build',
    kind: 'ask',
    body: 'Which approach should I take?',
    options: [opt('key:1', 'Option A'), opt('key:2', 'Option B', { broad: true }), opt('key:3', 'Option C')],
    episode: 'ep1',
    ...over,
  };
}

function next(d: RemoteDetail | null = detail(), over: Partial<NextResponse> = {}): NextResponse {
  return { taken_at: 't', queue: { pos: 1, total: 3 }, working: 2, detail: d, ...over };
}

function state(over: Partial<State> = {}): State {
  return {
    screen: 'session',
    next: next(),
    fleet: null,
    cursor: 0,
    confirming: null,
    lastOkAt: 1000,
    error: null,
    flash: null,
    ...over,
  };
}

const fleet: FleetResponse = {
  sessions: [
    { name: 's1', title: 'One', state: 'working', waiting: false, deferred: false },
    { name: 's2', title: 'Two', state: 'waiting', waiting: true, deferred: false },
  ],
};

describe('rows', () => {
  it('lists options, then defer, then skip', () => {
    expect(rows(state()).map((r) => r.id)).toEqual(['key:1', 'key:2', 'key:3', 'defer', 'skip']);
  });

  it('carries broad and suggested flags through', () => {
    const r = rows(state({ next: next(detail({ options: [opt('suggest:0', 'Go', { suggested: true })] })) }));
    expect(r[0]).toMatchObject({ id: 'suggest:0', suggested: true });
  });

  it('is empty with no detail', () => {
    expect(rows(state({ next: next(null) }))).toEqual([]);
  });

  it('lists sessions on the fleet screen', () => {
    expect(rows(state({ screen: 'fleet', fleet })).map((r) => r.id)).toEqual(['s1', 's2']);
  });
});

describe('reduce: cursor', () => {
  it('moves down and up, clamped', () => {
    let s = state();
    for (let i = 0; i < 10; i++) s = reduce(s, 'down').state;
    expect(s.cursor).toBe(4);
    for (let i = 0; i < 10; i++) s = reduce(s, 'up').state;
    expect(s.cursor).toBe(0);
  });

  it('moves through sessions on the fleet screen', () => {
    let s = state({ screen: 'fleet', fleet });
    s = reduce(s, 'down').state;
    s = reduce(s, 'down').state;
    expect(s.cursor).toBe(1);
  });

  it('clears confirming when the cursor moves', () => {
    const s = reduce(state({ cursor: 1 }), 'tap').state;
    expect(s.confirming).toBe('key:2');
    expect(reduce(s, 'down').state.confirming).toBeNull();
    expect(reduce(s, 'up').state.confirming).toBeNull();
  });
});

describe('reduce: tap', () => {
  it('answers a normal option at once', () => {
    const r = reduce(state({ cursor: 0 }), 'tap');
    expect(r.effect).toEqual({ kind: 'answer', session: 'alpha', episode: 'ep1', option: 'key:1' });
    expect(r.state.confirming).toBeNull();
  });

  it('asks for a second tap on a broad option', () => {
    const r = reduce(state({ cursor: 1 }), 'tap');
    expect(r.effect).toBeNull();
    expect(r.state.confirming).toBe('key:2');
  });

  it('answers a broad option on the second tap', () => {
    const first = reduce(state({ cursor: 1 }), 'tap').state;
    const r = reduce(first, 'tap');
    expect(r.effect).toEqual({ kind: 'answer', session: 'alpha', episode: 'ep1', option: 'key:2' });
    expect(r.state.confirming).toBeNull();
  });

  it('emits defer and skip', () => {
    expect(reduce(state({ cursor: 3 }), 'tap').effect).toEqual({ kind: 'defer', session: 'alpha' });
    expect(reduce(state({ cursor: 4 }), 'tap').effect).toEqual({ kind: 'skip', session: 'alpha' });
  });

  it('does nothing with no rows', () => {
    const r = reduce(state({ next: next(null) }), 'tap');
    expect(r.effect).toBeNull();
  });
});

describe('reduce: double and fleet', () => {
  it('switches to the fleet screen and loads it', () => {
    const r = reduce(state({ cursor: 2 }), 'double');
    expect(r.state.screen).toBe('fleet');
    expect(r.state.cursor).toBe(0);
    expect(r.effect).toEqual({ kind: 'loadFleet' });
  });

  it('returns from the fleet screen on double', () => {
    const r = reduce(state({ screen: 'fleet', fleet, cursor: 1 }), 'double');
    expect(r.state.screen).toBe('session');
    expect(r.effect).toBeNull();
  });

  it('opens a session on tap in the fleet and returns', () => {
    const r = reduce(state({ screen: 'fleet', fleet, cursor: 1 }), 'tap');
    expect(r.effect).toEqual({ kind: 'open', session: 's2' });
    expect(r.state.screen).toBe('session');
    expect(r.state.cursor).toBe(0);
  });
});

describe('error freezes input', () => {
  it('tap, up and down do nothing on the session screen', () => {
    const s = state({ error: 'x', cursor: 1 });
    for (const g of ['tap', 'up', 'down'] as const) {
      const r = reduce(s, g);
      expect(r.effect).toBeNull();
      expect(r.state).toEqual(s);
    }
  });

  it('tap, up and down do nothing on the fleet screen', () => {
    const s = state({ screen: 'fleet', fleet, error: 'x', cursor: 0 });
    for (const g of ['tap', 'up', 'down'] as const) {
      const r = reduce(s, g);
      expect(r.effect).toBeNull();
      expect(r.state).toEqual(s);
    }
  });

  it('double still switches screens', () => {
    expect(reduce(state({ error: 'x' }), 'double').state.screen).toBe('fleet');
    expect(reduce(state({ screen: 'fleet', fleet, error: 'x' }), 'double').state.screen).toBe('session');
  });

  it('render shows no option rows on either screen', () => {
    expect(render(state({ error: 'x' }), 2000).options).toBe('');
    const f = render(state({ screen: 'fleet', fleet, error: 'x' }), 2000);
    expect(f.options).toBe('');
    expect(f.header).toBe('lobby not reachable');
  });
});

describe('flash', () => {
  it('replaces the title on the left of the header', () => {
    const h = render(withFlash(state(), 'sent'), 2000).header;
    expect(h.length).toBe(COLS);
    expect(h.startsWith('sent')).toBe(true);
    expect(h).not.toContain('Fix the build');
    expect(h.endsWith('1/3')).toBe(true);
  });

  it('error takes precedence over flash', () => {
    expect(render(withFlash(state({ error: 'x' }), 'sent'), 2000).header).toBe('lobby not reachable');
  });

  it('withFlash null clears it', () => {
    expect(withFlash(withFlash(state(), 'sent'), null).flash).toBeNull();
  });

  it('applyNext clears it', () => {
    expect(applyNext(withFlash(state(), 'sent'), next(), 5000).flash).toBeNull();
  });
});

describe('applyNext', () => {
  it('keeps the cursor when the episode is unchanged', () => {
    const s = applyNext(state({ cursor: 2, confirming: 'key:2' }), next(), 5000);
    expect(s.cursor).toBe(2);
    expect(s.confirming).toBe('key:2');
    expect(s.lastOkAt).toBe(5000);
    expect(s.error).toBeNull();
  });

  it('resets it when the episode changes', () => {
    const s = applyNext(state({ cursor: 2, confirming: 'key:2' }), next(detail({ episode: 'ep2' })), 5000);
    expect(s.cursor).toBe(0);
    expect(s.confirming).toBeNull();
  });

  it('resets it when the session changes', () => {
    const s = applyNext(state({ cursor: 2 }), next(detail({ session: 'beta' })), 5000);
    expect(s.cursor).toBe(0);
  });

  it('clamps the cursor if the options shrink', () => {
    const d = detail({ options: [opt('key:1', 'A')] });
    const s = applyNext(state({ cursor: 4 }), next(d), 5000);
    expect(s.cursor).toBe(2);
  });

  it('clears a previous error', () => {
    expect(applyNext(state({ error: 'boom' }), next(), 5000).error).toBeNull();
  });
});

describe('render', () => {
  it('header is title plus pos/total, COLS wide', () => {
    const out = render(state(), 2000);
    expect(out.header.length).toBe(COLS);
    expect(out.header.startsWith('Fix the build')).toBe(true);
    expect(out.header.endsWith('1/3')).toBe(true);
  });

  it('shows -/total when the session is not in the queue', () => {
    const out = render(state({ next: next(detail(), { queue: { pos: 0, total: 3 } }) }), 2000);
    expect(out.header.endsWith('-/3')).toBe(true);
  });

  it('shows <n>s ago when the last success is stale', () => {
    const out = render(state({ lastOkAt: 1000 }), 1000 + 12_500);
    expect(out.header.endsWith('12s ago')).toBe(true);
  });

  it('keeps pos/total at exactly 10 seconds', () => {
    const out = render(state({ lastOkAt: 1000 }), 1000 + 10_000);
    expect(out.header.endsWith('1/3')).toBe(true);
  });

  it('shows lobby not reachable on error', () => {
    expect(render(state({ error: 'x' }), 2000).header).toBe('lobby not reachable');
  });

  it('body wraps to three lines and appends the note', () => {
    const long = 'word '.repeat(100).trim();
    const out = render(state({ next: next(detail({ body: long, note: 'careful' })) }), 2000);
    const lines = out.body.split('\n');
    expect(lines.length).toBe(4);
    expect(lines[2]!.endsWith('…')).toBe(true);
    expect(lines[3]).toBe('careful');
  });

  it('shows all clear with the working count and no options', () => {
    const out = render(state({ next: next(null, { working: 4 }) }), 2000);
    expect(out.header).toBe('all clear');
    expect(out.body).toBe('4 working');
    expect(out.options).toBe('');
  });

  it('prefixes the cursor row and marks suggested rows', () => {
    const d = detail({ options: [opt('suggest:0', 'Yes, go ahead', { suggested: true }), opt('key:1', 'No')] });
    const lines = render(state({ next: next(d), cursor: 0 }), 2000).options.split('\n');
    expect(lines[0]).toBe('> Yes, go ahead *');
    expect(lines[1]).toBe('  No');
  });

  it('replaces a confirming row label', () => {
    const lines = render(state({ cursor: 1, confirming: 'key:2' }), 2000).options.split('\n');
    expect(lines[1]).toBe('> tap again to confirm');
  });

  it('the option window scrolls to keep the cursor visible', () => {
    const at = (cursor: number) => render(state({ cursor }), 2000).options.split('\n');
    expect(at(0)).toEqual(['> Option A', '  Option B', '  Option C', '  defer', '  skip']);
    const down = at(4);
    expect(down.length).toBe(5);
    expect(down[4]).toBe('> skip');
  });

  it('scrolls through a long list', () => {
    const options = Array.from({ length: 8 }, (_, i) => opt(`key:${i}`, `Opt ${i}`));
    const lines = render(state({ next: next(detail({ options })), cursor: 6 }), 2000).options.split('\n');
    expect(lines.length).toBe(5);
    expect(lines.filter((l) => l.startsWith('> '))).toEqual(['> Opt 6']);
  });

  it('renders the fleet screen', () => {
    const out = render(state({ screen: 'fleet', fleet, cursor: 1 }), 2000);
    expect(out.header.startsWith('sessions')).toBe(true);
    const lines = out.options.split('\n');
    expect(lines[0]).toContain('s1');
    expect(lines[1]!.startsWith('> ')).toBe(true);
    expect(lines[1]).toContain('s2');
  });
});
