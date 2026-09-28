import { describe, it, expectTypeOf } from 'vitest';
import type { IssueLogEntry, LogEventType } from '../schemas';

describe('IssueLogEntry types', () => {
  it('event field is a LogEventType', () => {
    expectTypeOf<IssueLogEntry['event']>().toEqualTypeOf<LogEventType>();
  });

  it('LogEventType includes known events', () => {
    expectTypeOf<'text'>().toExtend<LogEventType>();
    expectTypeOf<'action'>().toExtend<LogEventType>();
    expectTypeOf<'subagent'>().toExtend<LogEventType>();
    expectTypeOf<'pr'>().toExtend<LogEventType>();
    expectTypeOf<'turn'>().toExtend<LogEventType>();
    expectTypeOf<'warn'>().toExtend<LogEventType>();
    expectTypeOf<'info'>().toExtend<LogEventType>();
    expectTypeOf<'error'>().toExtend<LogEventType>();
  });
});
