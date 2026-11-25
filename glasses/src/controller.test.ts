import { describe, expect, it, vi } from 'vitest';
import type { AnswerResult, ActionResult } from './api';
import { GET_TIMEOUT_MS, POST_TIMEOUT_MS, client } from './api';
import { createController, type Api } from './controller';
import type { NextResponse, RemoteDetail, State } from './types';
import { BAD_TOKEN, TAP_GUARD_MS } from './view';

function detail(over: Partial<RemoteDetail> = {}): RemoteDetail {
  return {
    session: 'alpha',
    title: 'T',
    kind: 'ask',
    body: 'b',
    options: [
      { id: 'key:1', label: 'A' },
      { id: 'key:2', label: 'B', broad: true },
    ],
    episode: 'ep1',
    ...over,
  };
}
const next = (d: RemoteDetail | null = detail()): NextResponse => ({
  taken_at: 't',
  queue: { pos: 1, total: 1 },
  working: 0,
  detail: d,
});

const initial = (): State => ({
  screen: 'session',
  next: null,
  fleet: null,
  cursor: 0,
  confirming: null,
  lastOkAt: 0,
  error: null,
  flash: null,
});

// A fake API whose calls are logged and can be held open.
function fakeApi() {
  const log: string[] = [];
  const gates: (() => void)[] = [];
  let hold = false;
  let nextResult: () => Promise<NextResponse> = async () => next();
  let answerResult: AnswerResult = 'sent';
  let actionResult: ActionResult = 'ok';
  const gate = () => (hold ? new Promise<void>((r) => gates.push(r)) : Promise.resolve());
  const api: Api = {
    async next(session?: string) {
      log.push(session ? `next:${session}` : 'next');
      await gate();
      return nextResult();
    },
    async fleet() {
      log.push('fleet');
      return { sessions: [{ name: 's1', title: 'One', state: 'working', waiting: false, deferred: false }] };
    },
    async answer(s: string, e: string, o: string) {
      log.push(`answer:${s}:${e}:${o}`);
      await gate();
      return answerResult;
    },
    async skip(s: string) {
      log.push(`skip:${s}`);
      return actionResult;
    },
    async defer(s: string) {
      log.push(`defer:${s}`);
      return actionResult;
    },
  };
  return {
    api,
    log,
    holdOpen: () => (hold = true),
    release: () => {
      hold = false;
      gates.splice(0).forEach((r) => r());
    },
    setNext: (f: () => Promise<NextResponse>) => (nextResult = f),
    setAnswer: (r: AnswerResult) => (answerResult = r),
    setAction: (r: ActionResult) => (actionResult = r),
  };
}

function setup(api: Api | null, onChange: (s: State) => void = () => {}, log: (s: string) => void = () => {}) {
  let t = 1000;
  const c = createController({ getApi: () => api, now: () => t, onChange, log }, initial());
  return { c, tick: (ms: number) => (t += ms) };
}

describe('robustness', () => {
  it('a stalled poll times out, sets the error, and a later poll runs', async () => {
    vi.useFakeTimers();
    try {
      let hang = true;
      const body = JSON.stringify(next());
      const f = (() => (hang ? new Promise<Response>(() => {}) : Promise.resolve(new Response(body, { status: 200 })))) as typeof fetch;
      const { c } = setup(client('http://h:1', 't', f));
      const p = c.poll();
      await vi.advanceTimersByTimeAsync(GET_TIMEOUT_MS);
      await p;
      expect(c.state().error).toBe('unreachable');
      hang = false;
      await c.poll();
      expect(c.state().error).toBeNull();
      expect(c.state().next?.detail?.session).toBe('alpha');
    } finally {
      vi.useRealTimers();
    }
  });

  it('an answer timeout flashes sent? check desk and gestures work afterwards', async () => {
    vi.useFakeTimers();
    try {
      let hang = true;
      const body = JSON.stringify(next());
      const f = ((u: string) =>
        String(u).includes('/answer') && hang
          ? new Promise<Response>(() => {})
          : Promise.resolve(new Response(String(u).includes('/next') ? body : '{}', { status: 200 }))) as typeof fetch;
      const { c, tick } = setup(client('http://h:1', 't', f));
      await c.poll();
      tick(TAP_GUARD_MS);
      const g = c.gesture('tap');
      await vi.advanceTimersByTimeAsync(POST_TIMEOUT_MS);
      await g;
      expect(c.state().flash).toBe('sent? check desk');
      hang = false;
      await c.gesture('tap');
      expect(c.state().flash).toBe('sent');
    } finally {
      vi.useRealTimers();
    }
  });

  it('survives a rejected task', async () => {
    const f = fakeApi();
    const logged: string[] = [];
    const { c, tick } = setup(f.api, () => {}, (s) => logged.push(s));
    await c.poll();
    tick(TAP_GUARD_MS);
    f.log.length = 0;
    const real = f.api.answer;
    f.api.answer = async () => {
      throw new Error('boom');
    };
    await c.gesture('tap');
    expect(logged.join()).toContain('boom');
    f.api.answer = real;
    await c.gesture('tap');
    expect(f.log).toContain('answer:alpha:ep1:key:1');
  });

  it('a throwing onChange is logged, not reported as unreachable', async () => {
    const f = fakeApi();
    const logged: string[] = [];
    const { c } = setup(f.api, () => {
      throw new Error('draw broke');
    }, (s) => logged.push(s));
    await c.poll();
    expect(c.state().error).toBeNull();
    expect(c.state().next?.detail?.session).toBe('alpha');
    expect(logged.join()).toContain('draw broke');
  });

  it('a tap queued behind a poll that brings a new episode posts what was on screen', async () => {
    const f = fakeApi();
    const { c, tick } = setup(f.api);
    await c.poll();
    tick(TAP_GUARD_MS);
    f.log.length = 0;
    f.holdOpen();
    const p = c.poll();
    const g = c.gesture('tap');
    f.setNext(async () => next(detail({ session: 'beta', episode: 'ep2' })));
    f.release();
    await Promise.all([p, g]);
    expect(f.log[0]).toBe('next');
    expect(f.log[1]).toBe('answer:alpha:ep1:key:1');
  });
});

describe('tap guard', () => {
  async function ready() {
    const f = fakeApi();
    const s = setup(f.api);
    await s.c.poll();
    s.tick(TAP_GUARD_MS);
    f.log.length = 0;
    return { f, ...s };
  }

  it('a new dialog replacing the old one just before a tap swallows the tap', async () => {
    const { f, c, tick } = await ready();
    f.setNext(async () => next(detail({ episode: 'ep2', options: [{ id: 'key:1', label: 'Yes, delete it' }] })));
    await c.poll();
    tick(200);
    await c.gesture('tap');
    expect(f.log).toEqual(['next']);
    tick(TAP_GUARD_MS);
    await c.gesture('tap');
    expect(f.log).toEqual(['next', 'answer:alpha:ep2:key:1', 'next']);
  });

  it('suggestions arriving under the replies do not swallow a tap on reply:0', async () => {
    const { f, c, tick } = await ready();
    const replies = [{ id: 'reply:0', label: 'Yes, go ahead' }];
    f.setNext(async () => next(detail({ kind: 'idle', episode: 'i1', options: replies })));
    await c.poll();
    tick(TAP_GUARD_MS);
    f.setNext(async () =>
      next(detail({ kind: 'idle', episode: 'i1', options: [...replies, { id: 'suggest:0', label: 'Open the PR', suggested: true }] })),
    );
    await c.poll();
    await c.gesture('tap');
    expect(f.log).toContain('answer:alpha:i1:reply:0');
  });

  it('suggestions arriving while the cursor is on defer keep it on defer', async () => {
    const { f, c, tick } = await ready();
    const replies = [{ id: 'reply:0', label: 'Yes, go ahead' }];
    f.setNext(async () => next(detail({ kind: 'idle', episode: 'i1', options: replies })));
    await c.poll();
    tick(TAP_GUARD_MS);
    await c.gesture('down'); // defer
    f.setNext(async () =>
      next(detail({ kind: 'idle', episode: 'i1', options: [...replies, { id: 'suggest:0', label: 'Open the PR', suggested: true }] })),
    );
    await c.poll();
    await c.gesture('tap');
    expect(f.log).toContain('defer:alpha');
    expect(f.log.some((l) => l.startsWith('answer'))).toBe(false);
  });

  it('up, down and double are not guarded', async () => {
    const { f, c } = await ready();
    f.setNext(async () => next(detail({ session: 'beta', episode: 'b1' })));
    await c.poll();
    await c.gesture('down');
    expect(c.state().cursor).toBe(1);
    await c.gesture('double');
    expect(c.state().screen).toBe('fleet');
  });
});

describe('polling', () => {
  it('applies a successful poll', async () => {
    const f = fakeApi();
    const { c } = setup(f.api);
    await c.poll();
    expect(c.state().next?.detail?.session).toBe('alpha');
    expect(c.state().error).toBeNull();
    expect(c.state().lastOkAt).toBe(1000);
  });

  it('sets an error on failure and clears it on the next success', async () => {
    const f = fakeApi();
    const { c } = setup(f.api);
    f.setNext(async () => {
      throw Object.assign(new Error('x'), { kind: 'unreachable' });
    });
    await c.poll();
    expect(c.state().error).toBe('unreachable');
    f.setNext(async () => next());
    await c.poll();
    expect(c.state().error).toBeNull();
  });

  it('an unauthorized poll shows the bad token error', async () => {
    const f = fakeApi();
    const { c } = setup(f.api);
    f.setNext(async () => {
      throw Object.assign(new Error('x'), { kind: 'unauthorized' });
    });
    await c.poll();
    expect(c.state().error).toBe(BAD_TOKEN);
  });

  it('with no api (no token) shows the bad token error without polling', async () => {
    const { c } = setup(null);
    await c.poll();
    expect(c.state().error).toBe(BAD_TOKEN);
  });

  it('never runs two polls at once', async () => {
    const f = fakeApi();
    const { c } = setup(f.api);
    f.holdOpen();
    const p1 = c.poll();
    const p2 = c.poll();
    expect(f.log).toEqual(['next']);
    f.release();
    await Promise.all([p1, p2]);
    expect(f.log).toEqual(['next']);
  });
});

describe('effects', () => {
  async function loaded() {
    const f = fakeApi();
    const s = setup(f.api);
    await s.c.poll();
    s.tick(TAP_GUARD_MS); // the first poll put rows under the cursor
    f.log.length = 0;
    return { f, ...s };
  }

  it('answers, then polls immediately, then flashes sent after the poll', async () => {
    const { f, c } = await loaded();
    await c.gesture('tap');
    expect(f.log).toEqual(['answer:alpha:ep1:key:1', 'next']);
    expect(c.state().flash).toBe('sent');
    await c.poll();
    expect(c.state().flash).toBeNull();
  });

  it('a broad option needs a second tap and the first tap posts nothing', async () => {
    const { f, c } = await loaded();
    await c.gesture('down');
    await c.gesture('tap');
    expect(f.log).toEqual([]);
    await c.gesture('tap');
    expect(f.log[0]).toBe('answer:alpha:ep1:key:2');
  });

  it.each([
    ['stuck', 'not accepted - try again'],
    ['draft', 'prompt has a draft'],
    ['unverified', 'sent? check desk'],
    ['typed', 'typed, not sent'],
    ['unsafe', 'reply refused'],
    ['gone', 'gone'],
    ['failed', 'failed'],
  ] as [AnswerResult, string][])('%s flashes %s and still polls', async (r, text) => {
    const { f, c } = await loaded();
    f.setAnswer(r);
    await c.gesture('tap');
    expect(c.state().flash).toBe(text);
    expect(f.log).toEqual(['answer:alpha:ep1:key:1', 'next']);
  });

  it('changed and busy redraw without a flash', async () => {
    const { f, c } = await loaded();
    f.setAnswer('changed');
    await c.gesture('tap');
    expect(c.state().flash).toBeNull();
    f.setAnswer('busy');
    await c.gesture('tap');
    expect(c.state().flash).toBeNull();
  });

  it('unauthorized answer sets the bad token error', async () => {
    const { f, c } = await loaded();
    f.setAnswer('unauthorized');
    await c.gesture('tap');
    expect(c.state().error).toBe(BAD_TOKEN);
  });

  it('ignores a tap while a POST is in flight', async () => {
    const { f, c } = await loaded();
    f.holdOpen();
    const first = c.gesture('tap');
    const second = c.gesture('tap');
    expect(f.log).toEqual(['answer:alpha:ep1:key:1']);
    f.release();
    await Promise.all([first, second]);
    expect(f.log.filter((l) => l.startsWith('answer'))).toHaveLength(1);
  });

  it('a poll tick while a POST is in flight is skipped', async () => {
    const { f, c } = await loaded();
    f.holdOpen();
    const g = c.gesture('tap');
    await c.poll();
    expect(f.log).toEqual(['answer:alpha:ep1:key:1']);
    f.release();
    await g;
    expect(f.log).toEqual(['answer:alpha:ep1:key:1', 'next']);
  });

  it('skip and defer post then poll', async () => {
    const { f, c } = await loaded();
    await c.gesture('down');
    await c.gesture('down');
    await c.gesture('tap');
    expect(f.log).toEqual(['defer:alpha', 'next']);
    await c.gesture('down');
    await c.gesture('tap');
    expect(f.log).toEqual(['defer:alpha', 'next', 'skip:alpha', 'next']);
  });

  it('a failed skip flashes failed', async () => {
    const { f, c } = await loaded();
    f.setAction('failed');
    for (let i = 0; i < 4; i++) await c.gesture(i < 3 ? 'down' : 'tap');
    expect(c.state().flash).toBe('failed');
  });

  it('input is frozen while an error is set', async () => {
    const { f, c } = await loaded();
    f.setNext(async () => {
      throw Object.assign(new Error('x'), { kind: 'unreachable' });
    });
    await c.poll();
    f.log.length = 0;
    await c.gesture('tap');
    expect(f.log).toEqual([]);
  });

  it('double loads the fleet; tapping a fleet row opens it and polls with that session', async () => {
    const { f, c } = await loaded();
    await c.gesture('double');
    expect(f.log).toEqual(['fleet']);
    expect(c.state().screen).toBe('fleet');
    expect(c.state().fleet?.sessions).toHaveLength(1);
    await c.gesture('tap');
    expect(f.log).toEqual(['fleet', 'next:s1']);
    expect(c.state().screen).toBe('session');
  });
});
