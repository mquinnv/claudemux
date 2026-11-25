// Wire types: mirror the JSON served by the lobby's /remote/ routes.

export type RemoteOption = {
  id: string; // e.g. "key:2", "reply:0", "suggest:1"
  label: string;
  broad?: boolean;
  suggested?: boolean;
};

export type RemoteKind = 'ask' | 'permit' | 'plan' | 'idle' | 'desk';

export type RemoteDetail = {
  session: string;
  title: string;
  kind: RemoteKind;
  body: string;
  note?: string;
  options: RemoteOption[];
  episode: string;
};

// GET /remote/next. queue.pos is 0 for a session opened from the fleet list
// that is not in the waiting queue.
export type NextResponse = {
  taken_at: string;
  queue: { pos: number; total: number };
  working: number;
  detail: RemoteDetail | null;
};

export type FleetSession = {
  name: string;
  title: string;
  state: string;
  waiting: boolean;
  deferred: boolean;
};

// GET /remote/fleet.
export type FleetResponse = { sessions: FleetSession[] };

// App state.

export type Row = { id: string; label: string; broad?: boolean; suggested?: boolean };

export type Screen = 'session' | 'fleet';

export type State = {
  screen: Screen;
  next: NextResponse | null;
  fleet: FleetResponse | null;
  cursor: number;
  confirming: string | null; // option id awaiting a second tap
  lastOkAt: number;
  error: string | null;
  flash: string | null;
};

export type Gesture = 'up' | 'down' | 'tap' | 'double';

export type Effect =
  | { kind: 'answer'; session: string; episode: string; option: string }
  | { kind: 'skip'; session: string }
  | { kind: 'defer'; session: string }
  | { kind: 'open'; session: string }
  | { kind: 'loadFleet' }
  | null;
