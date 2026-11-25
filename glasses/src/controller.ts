import type { ActionResult, AnswerResult } from './api';
import { ApiError } from './api';
import { actionOutcome, answerOutcome, type Outcome } from './outcome';
import type { Effect, FleetResponse, Gesture, NextResponse, State } from './types';
import { BAD_TOKEN, applyNext, reduce, withFlash } from './view';

export type Api = {
  next(session?: string): Promise<NextResponse>;
  fleet(): Promise<FleetResponse>;
  answer(session: string, episode: string, option: string): Promise<AnswerResult>;
  skip(session: string): Promise<ActionResult>;
  defer(session: string): Promise<ActionResult>;
};

export type Deps = {
  getApi: () => Api | null; // null when no token is stored
  now: () => number;
  onChange: (s: State) => void;
};

// Owns the app state and the sequencing rules: one task (poll or POST) runs
// at a time, a poll tick is skipped while anything is running, and a gesture
// that would POST is dropped while another POST is pending.
export function createController(deps: Deps, initial: State) {
  let state = initial;
  let active = 0;
  let posting = false;
  let tail: Promise<void> = Promise.resolve();

  const set = (s: State) => {
    state = s;
    deps.onChange(s);
  };

  function enqueue(task: () => Promise<void>): Promise<void> {
    const start = active === 0 ? task() : tail.then(task);
    active++;
    tail = start.finally(() => {
      active--;
    });
    return tail;
  }

  async function pollNow(session?: string): Promise<void> {
    const api = deps.getApi();
    if (!api) {
      set({ ...state, error: BAD_TOKEN });
      return;
    }
    try {
      set(applyNext(state, await api.next(session), deps.now()));
    } catch (e) {
      const kind = e instanceof ApiError ? e.kind : (e as { kind?: string }).kind;
      set({ ...state, error: kind === 'unauthorized' ? BAD_TOKEN : 'unreachable' });
    }
  }

  function show(o: Outcome) {
    if (o.error) set({ ...state, error: o.error });
    else if (o.flash) set(withFlash(state, o.flash));
  }

  async function run(e: NonNullable<Effect>): Promise<void> {
    const api = deps.getApi();
    if (!api) {
      set({ ...state, error: BAD_TOKEN });
      return;
    }
    switch (e.kind) {
      case 'loadFleet':
        try {
          set({ ...state, fleet: await api.fleet() });
        } catch (err) {
          const kind = err instanceof ApiError ? err.kind : 'failed';
          show(kind === 'unauthorized' ? { error: BAD_TOKEN } : { flash: 'failed' });
        }
        return;
      case 'open':
        await pollNow(e.session);
        return;
      case 'answer': {
        const o = answerOutcome(await api.answer(e.session, e.episode, e.option));
        await pollNow();
        // The flash goes up after the poll, which would otherwise clear it.
        show(o);
        return;
      }
      case 'skip':
      case 'defer': {
        const o = actionOutcome(await (e.kind === 'skip' ? api.skip(e.session) : api.defer(e.session)));
        await pollNow();
        show(o);
        return;
      }
    }
  }

  return {
    state: () => state,

    // A timer tick or an explicit refresh: skipped while a task is running.
    poll(): Promise<void> {
      if (active > 0) return Promise.resolve();
      return enqueue(() => pollNow());
    },

    gesture(g: Gesture): Promise<void> {
      const { state: next, effect } = reduce(state, g);
      if (effect && posting) return Promise.resolve(); // tap during a POST: ignored whole
      set(next);
      if (!effect) return Promise.resolve();
      posting = true;
      return enqueue(() => run(effect)).finally(() => {
        posting = false;
      });
    },

    // Redraw hook for the clock (stale-age text).
    refresh: () => deps.onChange(state),
  };
}
