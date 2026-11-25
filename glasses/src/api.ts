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

// The results the server may put in a 409 body. "sent" is deliberately absent:
// only a 200 means sent.
const REFUSALS = new Set<string>(['changed', 'stuck', 'draft', 'busy', 'unverified', 'typed', 'unsafe']);

type Reply = { status: number; text: string };

export function client(origin: string, token: string, f: typeof fetch = (...a) => fetch(...a)) {
  const base = origin.replace(/\/+$/, '');

  async function call(path: string, body?: unknown): Promise<Reply> {
    const headers: Record<string, string> = { Authorization: `Bearer ${token}` };
    const init: RequestInit = { headers, cache: 'no-store' };
    if (body !== undefined) {
      init.method = 'POST';
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
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
  }

  async function getJSON<T>(path: string): Promise<T> {
    const r = await call(path);
    if (r.status === 401) throw new ApiError('unauthorized');
    if (r.status !== 200) throw new ApiError('failed');
    try {
      return JSON.parse(r.text) as T;
    } catch {
      throw new ApiError('failed');
    }
  }

  async function post(path: string, body: unknown): Promise<Reply | ApiErrorKind> {
    try {
      return await call(path, body);
    } catch (e) {
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
