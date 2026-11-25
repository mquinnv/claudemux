import { getTextWidth } from '@evenrealities/pretext';
import { describe, expect, it } from 'vitest';
import { ELLIPSIS, WIDTH_PX } from './layout';
import type { FleetResponse, NextResponse, RemoteDetail, RemoteOption, State } from './types';
import { BAD_TOKEN, CURSOR_MARK, NO_MARK, TAP_GUARD_MS, applyNext, reduce, render, rows, withFlash } from './view';

const w = getTextWidth;
// A header line reaches the right edge: within two spaces of WIDTH_PX.
const atEdge = (line: string) => w(line) <= WIDTH_PX && w(line) > WIDTH_PX - 10;

describe('bad token error', () => {
  it('shows the bad token text in the header', () => {
    const s = { screen: 'session', next: null, fleet: null, cursor: 0, confirming: null, lastOkAt: 0, error: BAD_TOKEN, flash: null } as State;
    expect(render(s, 0).header).toBe('bad token - open setup');
  });
});

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

describe('reduce: a permit whose body is cut', () => {
  const yesNo = [opt('key:1', 'Yes'), opt('key:2', 'No')];
  const permit = (body: string, note?: string) =>
    state({ next: next(detail({ kind: 'permit', body, options: yesNo, ...(note ? { note } : {}) })) });
  const long = 'Bash command\n' + 'rm -rf build && make all && make test && make install '.repeat(6);

  it('answers Yes at once when the body fits', () => {
    const r = reduce(permit('Bash command\nls -la'), 'tap');
    expect(r.effect).toEqual({ kind: 'answer', session: 'alpha', episode: 'ep1', option: 'key:1' });
    expect(rows(permit('Bash command\nls -la'))[0]?.needsConfirm).toBeFalsy();
  });

  it('asks for a second tap on Yes when the body was cut, then answers', () => {
    const first = reduce(permit(long), 'tap');
    expect(first.effect).toBeNull();
    expect(first.state.confirming).toBe('key:1');
    expect(reduce(first.state, 'tap').effect).toEqual({ kind: 'answer', session: 'alpha', episode: 'ep1', option: 'key:1' });
  });

  it('marks every answer row, not defer or skip', () => {
    const r = rows(permit(long));
    expect(r.map((x) => !!x.needsConfirm)).toEqual([true, true, false, false]);
  });

  it('counts the note line: a body that fits three lines but not two is cut', () => {
    const three = 'Bash command\nfirst line of the command\nsecond line of the command';
    expect(rows(permit(three))[0]?.needsConfirm).toBeFalsy();
    expect(rows(permit(three, 'prompt has a draft'))[0]?.needsConfirm).toBe(true);
  });

  it('asks for a second tap when a character had to be replaced', () => {
    expect(rows(permit('Bash command\necho `whoami`'))[0]?.needsConfirm).toBe(true);
  });

  it('does not apply to other kinds', () => {
    const s = state({ next: next(detail({ kind: 'idle', body: long, options: yesNo })) });
    expect(reduce(s, 'tap').effect).toEqual({ kind: 'answer', session: 'alpha', episode: 'ep1', option: 'key:1' });
  });

  it('a broad row needs a second tap even with a fitting body', () => {
    const s = state({ next: next(detail({ kind: 'permit', body: 'ls', options: [opt('key:1', 'Yes'), opt('key:2', 'Always', { broad: true })] })), cursor: 1 });
    expect(rows(s)[1]?.needsConfirm).toBe(true);
    expect(reduce(s, 'tap').effect).toBeNull();
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
    expect(atEdge(h)).toBe(true);
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

  it('a new episode keeps the cursor on the same option id but drops a pending confirm', () => {
    const s = applyNext(state({ cursor: 1, confirming: 'key:2' }), next(detail({ episode: 'ep2' })), 5000);
    expect(s.cursor).toBe(1);
    expect(s.confirming).toBeNull();
  });

  it('a new episode without the option under the cursor resets it', () => {
    const d = detail({ episode: 'ep2', options: [opt('key:1', 'Yes'), opt('key:2', 'No')] });
    const s = applyNext(state({ cursor: 2 }), next(d), 5000);
    expect(s.cursor).toBe(0);
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

  it('follows defer when options shrink above it', () => {
    const d = detail({ options: [opt('key:1', 'A')] });
    const s = applyNext(state({ cursor: 3 }), next(d), 5000);
    expect(rows(s)[s.cursor]?.id).toBe('defer');
  });

  it('keeps the cursor on reply:0 when suggestions arrive below the replies', () => {
    const replies = [opt('reply:0', 'Yes, go ahead'), opt('reply:1', 'Continue')];
    const before = state({ next: next(detail({ kind: 'idle', options: replies })), cursor: 0 });
    const withSuggestions = detail({ kind: 'idle', options: [...replies, opt('suggest:0', 'Open the PR', { suggested: true })] });
    const s = applyNext(before, next(withSuggestions), 5000);
    expect(rows(s)[s.cursor]?.id).toBe('reply:0');
  });

  it('keeps the cursor on defer when suggestions are inserted above it', () => {
    const replies = [opt('reply:0', 'Yes, go ahead')];
    const before = state({ next: next(detail({ kind: 'idle', options: replies })), cursor: 1 });
    expect(rows(before)[1]?.id).toBe('defer');
    const more = detail({ kind: 'idle', options: [...replies, opt('suggest:0', 'A', { suggested: true }), opt('suggest:1', 'B', { suggested: true })] });
    const s = applyNext(before, next(more), 5000);
    expect(rows(s)[s.cursor]?.id).toBe('defer');
  });

  it('leaves the fleet screen cursor alone', () => {
    const s = applyNext(state({ screen: 'fleet', fleet, cursor: 1 }), next(detail({ session: 'beta' })), 5000);
    expect(s.cursor).toBe(1);
  });
});

describe('applyNext: tap guard', () => {
  const guarded = (s: State, now: number) => now < (s.tapGuardUntil ?? 0);

  it('is not set when the row under the cursor is unchanged', () => {
    const s = applyNext(state({ cursor: 0 }), next(), 5000);
    expect(guarded(s, 5000)).toBe(false);
  });

  it('is not set when suggestions arrive while the cursor is on reply:0', () => {
    const replies = [opt('reply:0', 'Yes, go ahead')];
    const before = state({ next: next(detail({ kind: 'idle', options: replies })) });
    const s = applyNext(before, next(detail({ kind: 'idle', options: [...replies, opt('suggest:0', 'A', { suggested: true })] })), 5000);
    expect(guarded(s, 5000)).toBe(false);
  });

  it('is set for TAP_GUARD_MS when a different row lands under the cursor', () => {
    const d = detail({ options: [opt('key:1', 'Other'), opt('key:2', 'B', { broad: true }), opt('key:3', 'C')] });
    const s = applyNext(state({ cursor: 0 }), next(d), 5000);
    expect(guarded(s, 5000)).toBe(true);
    expect(guarded(s, 5000 + TAP_GUARD_MS - 1)).toBe(true);
    expect(guarded(s, 5000 + TAP_GUARD_MS)).toBe(false);
  });

  it('is set when a new dialog with the same rows replaces the old one', () => {
    const s = applyNext(state({ cursor: 0 }), next(detail({ episode: 'ep2', body: 'a different command' })), 5000);
    expect(guarded(s, 5000)).toBe(true);
  });

  it('is set when the session changes', () => {
    const s = applyNext(state({ cursor: 0 }), next(detail({ session: 'beta' })), 5000);
    expect(guarded(s, 5000)).toBe(true);
  });

  it('is set when rows first appear', () => {
    const s = applyNext(state({ next: null }), next(), 5000);
    expect(guarded(s, 5000)).toBe(true);
  });

  it('is not set for a new episode while the cursor is on defer or skip', () => {
    const s = applyNext(state({ cursor: 4 }), next(detail({ episode: 'ep2' })), 5000);
    expect(rows(s)[s.cursor]?.id).toBe('skip');
    expect(guarded(s, 5000)).toBe(false);
  });

  it('clears a previous error', () => {
    expect(applyNext(state({ error: 'boom' }), next(), 5000).error).toBeNull();
  });
});

describe('render', () => {
  it('header is title plus pos/total, reaching the right edge', () => {
    const out = render(state(), 2000);
    expect(atEdge(out.header)).toBe(true);
    expect(out.header.startsWith('Fix the build')).toBe(true);
    expect(out.header.endsWith('1/3')).toBe(true);
  });

  it('a very long title is cut by pixels and the right part stays at the edge', () => {
    const title = 'Make the glasses remote lay out WWW MMM text by pixels '.repeat(3);
    for (const now of [2000, 1000 + 12_500]) {
      const h = render(state({ next: next(detail({ title })) }), now).header;
      expect(atEdge(h)).toBe(true);
      expect(h).toContain(ELLIPSIS);
      expect(h.endsWith(now === 2000 ? ' 1/3' : ' 12s ago')).toBe(true);
    }
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

  it('body wraps to three lines without a note', () => {
    const long = 'word '.repeat(100).trim();
    const lines = render(state({ next: next(detail({ body: long })) }), 2000).body.split('\n');
    expect(lines.length).toBe(3);
    expect(lines[2]!.endsWith(ELLIPSIS)).toBe(true);
  });

  it('with a note the body is at most three lines: two of text, then the note', () => {
    const long = 'word '.repeat(100).trim();
    const lines = render(state({ next: next(detail({ body: long, note: 'prompt has a draft' })) }), 2000).body.split('\n');
    expect(lines.length).toBe(3);
    expect(lines[1]!.endsWith(ELLIPSIS)).toBe(true);
    expect(lines[2]).toBe('prompt has a draft');
  });

  it('an idle body shows its last lines, marking the cut at the start, note last', () => {
    const long = 'START ' + 'word '.repeat(150) + 'the very end';
    const out = render(state({ next: next(detail({ kind: 'idle', body: long, note: 'careful' })) }), 2000);
    const lines = out.body.split('\n');
    expect(lines.length).toBe(3);
    expect(lines[0]!.startsWith(ELLIPSIS)).toBe(true);
    expect(lines.slice(0, 2).join(' ').endsWith('the very end')).toBe(true);
    expect(out.body).not.toContain('START');
    expect(lines[2]).toBe('careful');
  });

  it('every body line fits the width, even a line of wide characters', () => {
    const body = 'W'.repeat(46) + '\n' + 'm'.repeat(46) + ' ' + 'M'.repeat(10);
    for (const kind of ['permit', 'idle'] as const) {
      const lines = render(state({ next: next(detail({ kind, body, note: 'n' })) }), 2000).body.split('\n');
      expect(lines.length).toBeLessThanOrEqual(3);
      for (const l of lines) expect(w(l)).toBeLessThanOrEqual(WIDTH_PX);
    }
  });

  it('replaces a backtick, which the font lacks, with an apostrophe', () => {
    const d = detail({ title: 'run `x`', body: 'echo `whoami`', options: [opt('key:1', 'use `y`')] });
    const out = render(state({ next: next(d) }), 2000);
    for (const part of [out.header, out.body, out.options]) expect(part).not.toContain('`');
    expect(out.body).toBe("echo 'whoami'");
  });

  it('a non-idle body still shows its first lines', () => {
    const long = 'START ' + 'word '.repeat(60) + 'the very end';
    const out = render(state({ next: next(detail({ kind: 'permit', body: long })) }), 2000);
    expect(out.body.startsWith('START')).toBe(true);
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
    expect(lines[0]).toBe(`${CURSOR_MARK}Yes, go ahead *`);
    expect(lines[1]).toBe(`${NO_MARK}No`);
  });

  it('the cursor and non-cursor prefixes have the same pixel width', () => {
    expect(CURSOR_MARK).not.toBe(NO_MARK);
    expect(w(CURSOR_MARK)).toBe(w(NO_MARK));
    expect(w(CURSOR_MARK + 'Option')).toBe(w(NO_MARK + 'Option'));
  });

  it('replaces a confirming row label', () => {
    const lines = render(state({ cursor: 1, confirming: 'key:2' }), 2000).options.split('\n');
    expect(lines[1]).toBe(`${CURSOR_MARK}tap again to confirm`);
  });

  it('the option window scrolls to keep the cursor visible', () => {
    const at = (cursor: number) => render(state({ cursor }), 2000).options.split('\n');
    expect(at(0)).toEqual([`${CURSOR_MARK}Option A`, `${NO_MARK}Option B`, `${NO_MARK}Option C`, `${NO_MARK}defer`, `${NO_MARK}skip`]);
    const down = at(4);
    expect(down.length).toBe(5);
    expect(down[4]).toBe(`${CURSOR_MARK}skip`);
  });

  it('scrolls through a long list', () => {
    const options = Array.from({ length: 8 }, (_, i) => opt(`key:${i}`, `Opt ${i}`));
    const lines = render(state({ next: next(detail({ options })), cursor: 6 }), 2000).options.split('\n');
    expect(lines.length).toBe(5);
    expect(lines.filter((l) => l.startsWith(CURSOR_MARK))).toEqual([`${CURSOR_MARK}Opt 6`]);
  });

  it('cuts each long option row to one line by pixels, keeping the suggested marker', () => {
    const label = 'Yes, and also do the WWW MMM thing with a very long explanation attached to it';
    const options = Array.from({ length: 7 }, (_, i) => opt(`suggest:${i}`, `${i} ${label}`, { suggested: true }));
    const lines = render(state({ next: next(detail({ options })), cursor: 1 }), 2000).options.split('\n');
    expect(lines.length).toBe(5);
    for (const l of lines.slice(0, 4)) {
      expect(w(l)).toBeLessThanOrEqual(WIDTH_PX);
      expect(l.endsWith(`${ELLIPSIS} *`)).toBe(true);
    }
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
