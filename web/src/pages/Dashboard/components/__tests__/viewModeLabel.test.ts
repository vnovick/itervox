import { describe, expect, it } from 'vitest';
import { dashboardViewModes, viewModeLabel } from '../viewModeLabel';

describe('dashboard view modes', () => {
  it('places Deps directly after Agents when agent columns are visible', () => {
    expect(dashboardViewModes(true)).toEqual(['board', 'list', 'agents', 'deps', 'notifications']);
  });

  it('keeps Deps before Notifications when agent columns are hidden', () => {
    expect(dashboardViewModes(false)).toEqual(['board', 'list', 'deps', 'notifications']);
  });

  it('labels the Deps tab compactly', () => {
    expect(viewModeLabel('deps', 3)).toBe('Deps');
  });
});
