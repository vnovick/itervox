import { useLocation, useNavigate } from 'react-router';
import { useUIStore } from '../../../store/uiStore';
import { RequireScope } from '../RequireScope';
import { plainButtonClass } from '../../ui/button/buttonRecipe';

const CLASS = plainButtonClass('primary', 'md', { pill: true, extra: 'shrink-0 font-semibold' });

/**
 * #251 — the top bar's "+ New task" button (operator scope). The dialog is
 * mounted by the Dashboard, so on the Dashboard it opens in place; from any
 * other page it goes to the Dashboard with ?newTask=1, which
 * useNewTaskUrlOpen turns into the same open action.
 */
export function NewTaskButton() {
  const { pathname } = useLocation();
  const navigate = useNavigate();
  return (
    <RequireScope scope="operator">
      <button
        type="button"
        className={CLASS}
        title="Create a one-off task with its own instructions"
        onClick={() => {
          if (pathname === '/') useUIStore.getState().setNewTaskOpen(true);
          else void navigate('/?newTask=1');
        }}
      >
        + New task
      </button>
    </RequireScope>
  );
}
