import { describe, expect, it } from 'vitest';
import type { AnswerResult } from './api';
import { actionOutcome, answerOutcome } from './outcome';
import { BAD_TOKEN } from './view';

describe('answerOutcome', () => {
  const cases: [AnswerResult, { flash?: string; error?: string }][] = [
    ['sent', { flash: 'sent' }],
    ['changed', {}],
    ['busy', {}],
    ['gone', { flash: 'gone' }],
    ['stuck', { flash: 'not accepted - try again' }],
    ['draft', { flash: 'prompt has a draft' }],
    ['unverified', { flash: 'sent? check desk' }],
    ['typed', { flash: 'typed, not sent' }],
    ['unsafe', { flash: 'reply refused' }],
    ['failed', { flash: 'failed' }],
    ['unreachable', { flash: 'failed' }],
    ['unauthorized', { error: BAD_TOKEN }],
  ];
  it.each(cases)('%s', (r, want) => {
    expect(answerOutcome(r)).toEqual(want);
  });

  it('only sent says sent', () => {
    for (const [r, want] of cases) {
      if (r !== 'sent') expect(want.flash).not.toBe('sent');
    }
  });
});

describe('actionOutcome', () => {
  it('maps skip/defer results', () => {
    expect(actionOutcome('ok')).toEqual({});
    expect(actionOutcome('gone')).toEqual({ flash: 'gone' });
    expect(actionOutcome('failed')).toEqual({ flash: 'failed' });
    expect(actionOutcome('unreachable')).toEqual({ flash: 'failed' });
    expect(actionOutcome('unauthorized')).toEqual({ error: BAD_TOKEN });
  });
});
