import {
  OsEventTypeList,
  StartUpPageCreateResult,
  waitForEvenAppBridge,
} from '@evenrealities/even_hub_sdk';
import { client } from './api';
import { createController } from './controller';
import { changedRegions, gestureFor, type Drawn } from './device';
import { createLog } from './log';
import { mountSetup, showLog } from './setup';
import type { State } from './types';
import { render } from './view';

const POLL_MS = 2000;
const CLOCK_MS = 1000;

const logEl = document.getElementById('log');
const log = createLog(10, (lines) => {
  if (logEl) showLog(logEl, lines);
});

// Container geometry: header, body, options; options captures ring events.
const TEXT = (id: number, name: string, y: number, h: number, content: string, capture: number) => ({
  xPosition: 0,
  yPosition: y,
  width: 576,
  height: h,
  containerID: id,
  containerName: name,
  content,
  isEventCapture: capture,
});

const initialState: State = {
  screen: 'session',
  next: null,
  fleet: null,
  cursor: 0,
  confirming: null,
  lastOkAt: 0,
  error: null,
  flash: null,
};

async function main(): Promise<void> {
  log.add('waiting for bridge');
  const bridge = await waitForEvenAppBridge();

  let token = '';
  try {
    token = (await bridge.getLocalStorage('token')) ?? '';
  } catch (e) {
    log.add(`token read failed: ${e}`);
  }

  const setupEl = document.getElementById('setup');
  const origin = __REMOTE_ORIGIN__;
  if (!origin) log.add('built without a lobby origin');

  let drawn: Drawn | null = null;
  let drawing: Promise<void> = Promise.resolve();
  const draw = (s: State) => {
    const next = render(s, Date.now());
    const regions = changedRegions(drawn, next);
    if (regions.length === 0) return;
    drawn = next;
    drawing = drawing.then(async () => {
      for (const r of regions) {
        try {
          // textContainerUpgrade updates in place, with no flicker.
          await bridge.textContainerUpgrade({ containerID: r.id, containerName: r.name, content: r.content } as never);
        } catch (e) {
          log.add(`upgrade ${r.name} failed: ${e}`);
        }
      }
    });
  };

  const ctl = createController(
    {
      getApi: () => (token && origin ? client(origin, token) : null),
      now: Date.now,
      onChange: draw,
    },
    initialState,
  );

  const first = render(initialState, Date.now());
  const created = await bridge.createStartUpPageContainer({
    containerTotalNum: 3,
    textObject: [
      TEXT(1, 'hdr', 0, 32, first.header, 0),
      TEXT(2, 'body', 32, 96, first.body, 0),
      TEXT(3, 'opts', 128, 160, first.options, 1),
    ],
  } as never);
  if (created !== StartUpPageCreateResult.success) log.add(`startup page failed: ${created}`);
  drawn = first;

  // Mounted only now: saving a token polls, and a poll draws, which needs
  // the containers to exist.
  if (setupEl) {
    mountSetup(setupEl, {
      origin,
      hasToken: token !== '',
      saveToken: async (t) => {
        await bridge.setLocalStorage('token', t);
        token = t;
        log.add('token saved');
        void ctl.poll();
      },
    });
  }

  bridge.onEvenHubEvent((ev) => {
    if (ev.audioEvent) return;
    const e = ev.textEvent ?? ev.sysEvent ?? ev.listEvent;
    if (!e) return;
    const t = e.eventType;
    const g = gestureFor(t === undefined ? undefined : OsEventTypeList[t]);
    if (g) void ctl.gesture(g);
  });

  setInterval(() => void ctl.poll(), POLL_MS);
  setInterval(ctl.refresh, CLOCK_MS);
  void ctl.poll();
}

main().catch((e) => log.add(`fatal: ${e}`));
