import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { SearchInput } from '../SearchInput';

describe('SearchInput', () => {
  it('calls onChange as the user types and clears the value', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();

    function Harness() {
      const [value, setValue] = useState('');
      return (
        <SearchInput
          label="Search issues"
          placeholder="Search..."
          value={value}
          onChange={(next) => {
            onChange(next);
            setValue(next);
          }}
        />
      );
    }

    render(<Harness />);

    const input = screen.getByRole('searchbox', { name: /search issues/i });
    await user.type(input, 'ENG');
    expect(input).toHaveValue('ENG');
    expect(onChange).toHaveBeenLastCalledWith('ENG');
    await user.click(screen.getByRole('button', { name: /clear search/i }));
    expect(input).toHaveValue('');
    expect(onChange).toHaveBeenLastCalledWith('');
  });
});
