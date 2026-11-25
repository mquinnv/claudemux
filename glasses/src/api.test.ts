import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, GET_TIMEOUT_MS, POST_TIMEOUT_MS, client } from './api';

const hang: typeof fetch = (() => new Promise<Response>(() => {})) as typeof fetch;

describe('timeouts', () => {
  afterEach(() => vi.useRealTimers());

  it('exports the durations', () => {
    expect(GET_TIMEOUT_MS).toBe(5000);
    expect(POST_TIMEOUT_MS).toBe(8000);
  });

  it('a stalled GET is unreachable after the GET timeout', async () => {
    vi.useFakeTimers();
    const c = client('http://h:1', 't', hang);
    const p = c.next().catch((e) => e);
    await vi.advanceTimersByTimeAsync(GET_TIMEOUT_MS - 1);
    let settled = false;
    void p.then(() => (settled = true));
    await Promise.resolve();
    expect(settled).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(await p).toMatchObject({ kind: 'unreachable' });
  });

  it('a stalled answer POST is unverified, never failed or sent', async () => {
    vi.useFakeTimers();
    const p = client('http://h:1', 't', hang).answer('s', 'e', 'o');
    await vi.advanceTimersByTimeAsync(POST_TIMEOUT_MS);
    expect(await p).toBe('unverified');
  });

  it('a stalled skip or defer is failed', async () => {
    vi.useFakeTimers();
    const c = client('http://h:1', 't', hang);
    const a = c.skip('s');
    const b = c.defer('s');
    await vi.advanceTimersByTimeAsync(POST_TIMEOUT_MS);
    expect(await a).toBe('failed');
    expect(await b).toBe('failed');
  });

  it('aborts the request when it times out', async () => {
    vi.useFakeTimers();
    let signal: AbortSignal | undefined;
    const f = ((_u: string, init?: RequestInit) => {
      signal = init?.signal ?? undefined;
      return new Promise<Response>(() => {});
    }) as unknown as typeof fetch;
    const p = client('http://h:1', 't', f).fleet().catch(() => {});
    await vi.advanceTimersByTimeAsync(GET_TIMEOUT_MS);
    await p;
    expect(signal?.aborted).toBe(true);
  });
});

type Call = { url: string; init: RequestInit };

function fake(status: number, body: unknown = {}, calls: Call[] = []): typeof fetch {
  return (async (url: string | URL | Request, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} });
    const text = typeof body === 'string' ? body : JSON.stringify(body);
    return new Response(status === 204 ? null : text, { status });
  }) as typeof fetch;
}

const boom: typeof fetch = (async () => {
  throw new TypeError('network down');
}) as typeof fetch;

const O = 'http://100.64.0.15:7474';

describe('requests', () => {
  it('sends the bearer token on GET and POST', async () => {
    const calls: Call[] = [];
    const c = client(O, 'tok', fake(200, { result: 'sent' }, calls));
    await c.fleet();
    await c.answer('s', 'e', 'key:1');
    for (const call of calls) {
      expect(new Headers(call.init.headers).get('Authorization')).toBe('Bearer tok');
    }
  });

  it('adds the session to next, URL-encoded', async () => {
    const calls: Call[] = [];
    const c = client(O, 't', fake(200, {}, calls));
    await c.next('a b/c');
    await c.next();
    expect(calls[0]?.url).toBe(`${O}/remote/next?session=a%20b%2Fc`);
    expect(calls[1]?.url).toBe(`${O}/remote/next`);
  });

  it('posts the answer as JSON', async () => {
    const calls: Call[] = [];
    await client(O, 't', fake(200, { result: 'sent' }, calls)).answer('s1', 'ep', 'reply:0');
    expect(calls[0]?.url).toBe(`${O}/remote/answer`);
    expect(calls[0]?.init.method).toBe('POST');
    expect(JSON.parse(String(calls[0]?.init.body))).toEqual({ session: 's1', episode: 'ep', option: 'reply:0' });
  });

  it('tolerates a trailing slash on the origin', async () => {
    const calls: Call[] = [];
    await client(`${O}/`, 't', fake(200, {}, calls)).fleet();
    expect(calls[0]?.url).toBe(`${O}/remote/fleet`);
  });
});

describe('answer results', () => {
  it('200 is sent', async () => {
    expect(await client(O, 't', fake(200, { result: 'sent' })).answer('s', 'e', 'o')).toBe('sent');
  });

  it.each(['changed', 'stuck', 'draft', 'busy', 'unverified', 'typed', 'unsafe'])('409 %s maps to itself', async (r) => {
    expect(await client(O, 't', fake(409, { result: r })).answer('s', 'e', 'o')).toBe(r);
  });

  it('never reports sent for a 409, even with a sent body', async () => {
    expect(await client(O, 't', fake(409, { result: 'sent' })).answer('s', 'e', 'o')).toBe('failed');
  });

  it('409 with an unknown or unparsable body is failed', async () => {
    expect(await client(O, 't', fake(409, { result: 'wat' })).answer('s', 'e', 'o')).toBe('failed');
    expect(await client(O, 't', fake(409, 'not json')).answer('s', 'e', 'o')).toBe('failed');
  });

  it('404 is gone, 401 unauthorized, 500 failed', async () => {
    expect(await client(O, 't', fake(404, { result: 'gone' })).answer('s', 'e', 'o')).toBe('gone');
    expect(await client(O, 't', fake(401, '')).answer('s', 'e', 'o')).toBe('unauthorized');
    expect(await client(O, 't', fake(500, '')).answer('s', 'e', 'o')).toBe('failed');
  });

  it('a thrown fetch is unreachable', async () => {
    expect(await client(O, 't', boom).answer('s', 'e', 'o')).toBe('unreachable');
  });
});

describe('next, fleet, skip, defer', () => {
  it('next returns the parsed body', async () => {
    const body = { taken_at: 't', queue: { pos: 1, total: 1 }, working: 0, detail: null };
    expect(await client(O, 't', fake(200, body)).next()).toEqual(body);
  });

  it('next and fleet throw ApiError with a kind', async () => {
    await expect(client(O, 't', fake(401, '')).next()).rejects.toMatchObject({ kind: 'unauthorized' });
    await expect(client(O, 't', boom).fleet()).rejects.toMatchObject({ kind: 'unreachable' });
    await expect(client(O, 't', fake(500, '')).next()).rejects.toBeInstanceOf(ApiError);
    await expect(client(O, 't', fake(200, 'garbage')).fleet()).rejects.toMatchObject({ kind: 'failed' });
  });

  it('skip and defer post the session and map status', async () => {
    const calls: Call[] = [];
    const c = client(O, 't', fake(200, {}, calls));
    expect(await c.skip('s1')).toBe('ok');
    expect(await c.defer('s2')).toBe('ok');
    expect(calls.map((x) => x.url)).toEqual([`${O}/remote/skip`, `${O}/remote/defer`]);
    expect(JSON.parse(String(calls[1]?.init.body))).toEqual({ session: 's2' });
    expect(await client(O, 't', fake(404, {})).skip('s')).toBe('gone');
    expect(await client(O, 't', fake(401, '')).defer('s')).toBe('unauthorized');
    expect(await client(O, 't', fake(500, '')).skip('s')).toBe('failed');
    expect(await client(O, 't', boom).defer('s')).toBe('unreachable');
  });
});
