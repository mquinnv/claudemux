import type { FleetResponse, NextResponse } from './types';

export type AnswerResult =
  | 'sent'
  | 'changed'
  | 'stuck'
  | 'draft'
  | 'busy'
  | 'unverified'
  | 'typed'
  | 'unsafe'
  | 'gone'
  | 'unauthorized'
  | 'unreachable'
  | 'failed';

export type ActionResult = 'ok' | 'gone' | 'unauthorized' | 'unreachable' | 'failed';

export type ApiErrorKind = 'unauthorized' | 'unreachable' | 'failed';

export class ApiError extends Error {
  readonly kind: ApiErrorKind;
  constructor(kind: ApiErrorKind) {
    super(kind);
    this.kind = kind;
  }
}

export const GET_TIMEOUT_MS = 5000;
export const POST_TIMEOUT_MS = 8000;

// Internal: a request that did not finish in time. Its outcome is unknown.
class TimedOut extends Error {}

// The results the server may put in a 409 body. "sent" is deliberately absent:
// only a 200 means sent.
const REFUSALS = new Set<string>(['changed', 'stuck', 'draft', 'busy', 'unverified', 'typed', 'unsafe']);

type Reply = { status: number; text: string };

export function client(origin: string, token: string, f: typeof fetch = (...a) => fetch(...a)) {
  const base = origin.replace(/\/+$/, '');

  async function call(path: string, body?: unknown): Promise<Reply> {
    const headers: Record<string, string> = { Authorization: `Bearer ${token}` };
    const ctl = new AbortController();
    const init: RequestInit = { headers, cache: 'no-store', signal: ctl.signal };
    let ms = GET_TIMEOUT_MS;
    if (body !== undefined) {
      init.method = 'POST';
      ms = POST_TIMEOUT_MS;
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    // The race matters as much as the abort: a stalled connection may never
    // settle the fetch at all, and the whole request (body included) is timed.
    let timer: ReturnType<typeof setTimeout> | undefined;
    const timeout = new Promise<never>((_, reject) => {
      timer = setTimeout(() => {
        ctl.abort();
        reject(new TimedOut());
      }, ms);
    });
    const work = (async (): Promise<Reply> => {
      let res: Response;
      try {
        res = await f(base + path, init);
      } catch {
        throw new ApiError('unreachable');
      }
      let text = '';
      try {
        text = await res.text();
      } catch {
        // A body we cannot read is treated like an empty one.
      }
      return { status: res.status, text };
    })();
    work.catch(() => {}); // a late failure after the timeout is not unhandled
    try {
      return await Promise.race([work, timeout]);
    } finally {
      clearTimeout(timer);
    }
  }

  async function getJSON<T>(path: string): Promise<T> {
    let r: Reply;
    try {
      r = await call(path);
    } catch (e) {
      throw e instanceof TimedOut ? new ApiError('unreachable') : e;
    }
    if (r.status === 401) throw new ApiError('unauthorized');
    if (r.status !== 200) throw new ApiError('failed');
    try {
      return JSON.parse(r.text) as T;
    } catch {
      throw new ApiError('failed');
    }
  }

  async function post(path: string, body: unknown): Promise<Reply | ApiErrorKind | 'timeout'> {
    try {
      return await call(path, body);
    } catch (e) {
      if (e instanceof TimedOut) return 'timeout';
      return e instanceof ApiError ? e.kind : 'failed';
    }
  }

  function refusal(text: string): AnswerResult {
    try {
      const r = (JSON.parse(text) as { result?: unknown }).result;
      return typeof r === 'string' && REFUSALS.has(r) ? (r as AnswerResult) : 'failed';
    } catch {
      return 'failed';
    }
  }

  async function action(path: string, session: string): Promise<ActionResult> {
    const r = await post(path, { session });
    if (typeof r === 'string') return r === 'unauthorized' || r === 'unreachable' ? r : 'failed';
    if (r.status === 200) return 'ok';
    if (r.status === 404) return 'gone';
    if (r.status === 401) return 'unauthorized';
    return 'failed';
  }

  return {
    next(session?: string): Promise<NextResponse> {
      const q = session ? `?session=${encodeURIComponent(session)}` : '';
      return getJSON<NextResponse>(`/remote/next${q}`);
    },
    fleet(): Promise<FleetResponse> {
      return getJSON<FleetResponse>('/remote/fleet');
    },
    async answer(session: string, episode: string, option: string): Promise<AnswerResult> {
      const r = await post('/remote/answer', { session, episode, option });
      // A timeout or a dropped connection gives no status: the request may
      // have reached the lobby and the key may have gone out.
      if (r === 'timeout' || r === 'unreachable') return 'unverified';
      if (typeof r === 'string') return r;
      if (r.status === 200) return 'sent';
      if (r.status === 409) return refusal(r.text);
      if (r.status === 404) return 'gone';
      if (r.status === 401) return 'unauthorized';
      return 'failed';
    },
    skip: (session: string) => action('/remote/skip', session),
    defer: (session: string) => action('/remote/defer', session),
  };
}
