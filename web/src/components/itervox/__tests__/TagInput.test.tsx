import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { TagInput } from '../TagInput';

describe('TagInput', () => {
  it('uses the shared tag chip and pill Add button by default', async () => {
    const onChange = vi.fn();
    render(<TagInput chips={['Todo']} onChange={onChange} />);
    expect(screen.getByText('Todo').closest('span')).toHaveClass('tag-chip');
    const add = screen.getByRole('button', { name: 'Add' });
    expect(add).toHaveClass('rounded-full');
    await userEvent.type(screen.getByPlaceholderText('+ Add state'), 'Done{Enter}');
    expect(onChange).toHaveBeenCalledWith(['Todo', 'Done']);
  });

  it('keeps caller classes when they are passed', () => {
    render(
      <TagInput
        chips={['bug']}
        onChange={vi.fn()}
        chipClassName="custom-chip"
        addButtonClassName="custom-add"
      />,
    );
    const chip = screen.getByText('bug').closest('span');
    expect(chip).toHaveClass('custom-chip');
    expect(chip).not.toHaveClass('tag-chip');
    expect(screen.getByRole('button', { name: 'Add' })).toHaveClass('custom-add');
  });
});
