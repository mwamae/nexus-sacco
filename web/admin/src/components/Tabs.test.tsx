import { useState } from 'react';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect } from 'vitest';
import { axe } from 'vitest-axe';

import { Tabs } from './Tabs';

type Id = 'overview' | 'profile' | 'accounts';

function Harness({ initial = 'overview' as Id }: { initial?: Id } = {}) {
  const [value, setValue] = useState<Id>(initial);
  return (
    <Tabs
      ariaLabel="Member sections"
      tabs={[
        { id: 'overview', label: 'Overview' },
        { id: 'profile',  label: 'Profile' },
        { id: 'accounts', label: 'Accounts' },
      ]}
      value={value}
      onChange={setValue}
    >
      {(activeId) => <div data-testid="active">{activeId}</div>}
    </Tabs>
  );
}

describe('<Tabs> — WAI-ARIA tab pattern', () => {
  it('exposes correct roles + aria wiring', () => {
    render(<Harness />);
    const tablist = screen.getByRole('tablist');
    expect(tablist).toHaveAttribute('aria-label', 'Member sections');

    const tabs = screen.getAllByRole('tab');
    expect(tabs).toHaveLength(3);

    // The active tab is the only one in the document Tab sequence
    // (roving tabindex). Inactive tabs are still reachable via arrow
    // keys but skipped by Shift+Tab / Tab.
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true');
    expect(tabs[0]).toHaveAttribute('tabindex', '0');
    expect(tabs[1]).toHaveAttribute('aria-selected', 'false');
    expect(tabs[1]).toHaveAttribute('tabindex', '-1');

    // The active panel is wired back to its tab via aria-labelledby.
    const panel = screen.getByRole('tabpanel');
    expect(panel.getAttribute('aria-labelledby')).toBe(tabs[0].id);
    expect(tabs[0].getAttribute('aria-controls')).toBe(panel.id);
  });

  it('the active tab is reachable with the Tab key (was: focus-trapped <div>)', async () => {
    const user = userEvent.setup();
    render(
      <>
        <button data-testid="before">before</button>
        <Harness />
      </>,
    );

    // Tabbing forward from a sibling button must land on the active
    // tab — the old <div className="tab"> wasn't keyboard-focusable
    // at all, which is the regression this test pins.
    screen.getByTestId('before').focus();
    await user.tab();
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveFocus();
  });

  it('arrow keys move focus, Home / End jump to ends, activation is manual', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const tabs = screen.getAllByRole('tab');
    tabs[0].focus();

    await user.keyboard('{ArrowRight}');
    expect(tabs[1]).toHaveFocus();
    // Manual activation — focus moved, but the selected tab and panel
    // haven't changed yet.
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByTestId('active')).toHaveTextContent('overview');

    await user.keyboard('{ArrowRight}');
    expect(tabs[2]).toHaveFocus();

    // Wrap-around.
    await user.keyboard('{ArrowRight}');
    expect(tabs[0]).toHaveFocus();

    await user.keyboard('{End}');
    expect(tabs[2]).toHaveFocus();
    await user.keyboard('{Home}');
    expect(tabs[0]).toHaveFocus();

    // Enter activates the focused tab.
    await user.keyboard('{ArrowRight}');
    await user.keyboard('{Enter}');
    expect(tabs[1]).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByTestId('active')).toHaveTextContent('profile');
  });

  it('aria-selected + roving tabindex update together when the value changes', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const tabs = screen.getAllByRole('tab');

    // Clicking a tab activates it (preserves the existing click affordance).
    await user.click(tabs[2]);
    expect(tabs[2]).toHaveAttribute('aria-selected', 'true');
    expect(tabs[2]).toHaveAttribute('tabindex', '0');
    expect(tabs[0]).toHaveAttribute('aria-selected', 'false');
    expect(tabs[0]).toHaveAttribute('tabindex', '-1');
  });

  it('axe-core finds zero accessibility violations on the new structure', async () => {
    const { container } = render(<Harness />);
    const results = await axe(container);
    expect(results.violations).toEqual([]);
  });

  it('before/after delta — concrete defects in the old <div onClick> markup that are now fixed', () => {
    // Render the legacy markup that shipped before this refactor, then
    // assert the specific DOM properties that broke keyboard / screen
    // reader access. Each `expect(legacy)` here corresponds to a
    // matching `expect(newTabs)` further up that proves the fix.
    //
    // We intentionally measure these by hand rather than relying on
    // axe-core's default ruleset, because most of the violations are
    // "wcag2aaa" / "best-practice" tier and not enabled in axe's
    // default profile — so an axe-only assertion would under-report
    // the real damage the old markup caused.
    function Legacy() {
      const [value, setValue] = useState<Id>('overview');
      return (
        <div className="tabs">
          {(['overview', 'profile', 'accounts'] as Id[]).map((id) => (
            // eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions
            <div
              key={id}
              className="tab"
              data-active={value === id || undefined}
              onClick={() => setValue(id)}
            >
              {id}
            </div>
          ))}
        </div>
      );
    }
    const { container } = render(<Legacy />);

    // 1. No tablist landmark → screen readers can't announce "tab list
    //    with 3 tabs". The new component has role="tablist".
    expect(container.querySelector('[role="tablist"]')).toBeNull();
    // 2. No role="tab" anywhere → screen readers see plain divs.
    expect(container.querySelectorAll('[role="tab"]')).toHaveLength(0);
    // 3. None of the "tab" divs are keyboard focusable (no tabindex,
    //    no native button). Tab key skips the entire strip.
    const legacyTabs = container.querySelectorAll('.tab');
    expect(legacyTabs).toHaveLength(3);
    legacyTabs.forEach((el) => {
      expect(el.getAttribute('tabindex')).toBeNull();
      expect(el.tagName.toLowerCase()).not.toBe('button');
    });
    // 4. No aria-selected → AT users have no way to know which tab is
    //    currently chosen.
    expect(container.querySelectorAll('[aria-selected]')).toHaveLength(0);
    // 5. No aria-controls / aria-labelledby pairing → no relationship
    //    between the tab strip and the panel below it.
    expect(container.querySelectorAll('[aria-controls]')).toHaveLength(0);
  });
});
