// WAI-ARIA tab pattern.
//
// Replaces the ad-hoc `<div className="tab" onClick={…}>` strips that
// were scattered across pages. Same visual treatment (drop-in via the
// existing `.tabs` / `.tab` classes) but:
//   * <button role="tab"> instead of <div> → keyboard focusable +
//     announced by screen readers
//   * tabIndex roving (only the active tab is in the Tab sequence; the
//     rest are reached via arrow keys)
//   * Manual activation — ArrowLeft / ArrowRight / Home / End move
//     focus, Enter / Space activates. Manual rather than automatic
//     because most consumers of this component trigger network fetches
//     on tab change, and we don't want arrowing through to spam them.
//   * Each tab wired to its panel via aria-controls + the panel's
//     aria-labelledby — without that pairing screen readers can't tell
//     a "tab" from an arbitrary button.

import {
  useCallback,
  useId,
  useRef,
  type CSSProperties,
  type KeyboardEvent,
  type ReactNode,
} from 'react';

export type TabDef<ID extends string = string> = {
  id: ID;
  label: ReactNode;
};

export type TabsProps<ID extends string> = {
  tabs: ReadonlyArray<TabDef<ID>>;
  value: ID;
  onChange: (id: ID) => void;
  // Accessible name for the tablist (required by ARIA). Examples:
  // "Member sections", "Tenant settings". Used by screen readers to
  // announce what set of tabs the user has landed in.
  ariaLabel: string;
  // Optional style overrides for the existing wrappers. Defaults
  // match the most-common page layout (padding: 0 14px on the strip,
  // padding: 14 on the panel).
  tablistStyle?: CSSProperties;
  panelStyle?: CSSProperties;
  // Render the active panel. Receives the active id so the consumer
  // can do its existing inline conditional rendering.
  children: (activeId: ID) => ReactNode;
};

const DEFAULT_TABLIST_STYLE: CSSProperties = { padding: '0 14px' };
const DEFAULT_PANEL_STYLE: CSSProperties = { padding: 14 };

export function Tabs<ID extends string>({
  tabs,
  value,
  onChange,
  ariaLabel,
  tablistStyle,
  panelStyle,
  children,
}: TabsProps<ID>) {
  // useId gives each Tabs instance its own id namespace, so two strips
  // on the same page (e.g. PlatformDashboard's nested tabs) don't
  // collide on `tab-overview` etc.
  const base = useId();
  const tabDomId = useCallback((id: string) => `${base}tab-${id}`, [base]);
  const panelDomId = useCallback((id: string) => `${base}panel-${id}`, [base]);

  const buttonRefs = useRef<Record<string, HTMLButtonElement | null>>({});

  const onTabKeyDown = useCallback((e: KeyboardEvent<HTMLButtonElement>, currentId: ID) => {
    const idx = tabs.findIndex((t) => t.id === currentId);
    if (idx < 0) return;
    let nextIdx = -1;
    switch (e.key) {
      case 'ArrowLeft':
        nextIdx = (idx - 1 + tabs.length) % tabs.length;
        break;
      case 'ArrowRight':
        nextIdx = (idx + 1) % tabs.length;
        break;
      case 'Home':
        nextIdx = 0;
        break;
      case 'End':
        nextIdx = tabs.length - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    const nextId = tabs[nextIdx].id;
    // Manual activation — move focus but don't switch panels until the
    // user presses Enter / Space (which fires the button's onClick).
    buttonRefs.current[nextId]?.focus();
  }, [tabs]);

  return (
    <>
      <div
        role="tablist"
        aria-label={ariaLabel}
        className="tabs"
        style={tablistStyle ?? DEFAULT_TABLIST_STYLE}
      >
        {tabs.map((t) => {
          const active = t.id === value;
          return (
            <button
              key={t.id}
              ref={(el) => { buttonRefs.current[t.id] = el; }}
              type="button"
              role="tab"
              id={tabDomId(t.id)}
              aria-selected={active}
              aria-controls={panelDomId(t.id)}
              tabIndex={active ? 0 : -1}
              className="tab"
              data-active={active || undefined}
              onClick={() => onChange(t.id)}
              onKeyDown={(e) => onTabKeyDown(e, t.id)}
            >
              {t.label}
            </button>
          );
        })}
      </div>
      <div
        role="tabpanel"
        id={panelDomId(value)}
        aria-labelledby={tabDomId(value)}
        tabIndex={0}
        style={panelStyle ?? DEFAULT_PANEL_STYLE}
      >
        {children(value)}
      </div>
    </>
  );
}
