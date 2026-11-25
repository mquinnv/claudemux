import type { ActionResult, AnswerResult } from './api';
import { BAD_TOKEN } from './view';

// What the screen should do after a POST: flash a message for one poll, set
// a blocking error, or (empty) just redraw from the next poll.
export type Outcome = { flash?: string; error?: string };

const ANSWER_FLASH: Partial<Record<AnswerResult, string>> = {
  sent: 'sent',
  gone: 'gone',
  stuck: 'not accepted - try again',
  draft: 'prompt has a draft',
  unverified: 'sent? check desk',
  typed: 'typed, not sent',
  unsafe: 'reply refused',
  failed: 'failed',
  unreachable: 'failed',
};

export function answerOutcome(r: AnswerResult): Outcome {
  if (r === 'unauthorized') return { error: BAD_TOKEN };
  const flash = ANSWER_FLASH[r];
  return flash === undefined ? {} : { flash };
}

export function actionOutcome(r: ActionResult): Outcome {
  switch (r) {
    case 'ok':
      return {};
    case 'gone':
      return { flash: 'gone' };
    case 'unauthorized':
      return { error: BAD_TOKEN };
    default:
      return { flash: 'failed' };
  }
}
