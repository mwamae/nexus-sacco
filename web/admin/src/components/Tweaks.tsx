// Floating tweaks panel — draggable, scrollable, persists to localStorage.
// Adapted from the mzizi prototype; stripped of loan/dashboard tweaks we
// don't need yet and reduced to UI-affecting prefs only.

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react';

const PANEL_STYLE = `
  .twk-trigger{position:fixed;right:16px;bottom:16px;z-index:2147483646;
    width:36px;height:36px;display:grid;place-items:center;
    background:rgba(250,249,247,.92);color:#29261b;
    border:.5px solid rgba(255,255,255,.6);border-radius:10px;
    box-shadow:0 1px 0 rgba(255,255,255,.5) inset,0 6px 20px rgba(0,0,0,.18);
    cursor:pointer;font-size:14px;font-weight:600;
    -webkit-backdrop-filter:blur(24px) saturate(160%);backdrop-filter:blur(24px) saturate(160%)}
  .twk-trigger:hover{background:rgba(255,255,255,.98)}
  [data-theme="dark"] .twk-trigger{background:rgba(28,26,22,.85);color:#f3efe5;
    border-color:rgba(255,255,255,.08)}
  .twk-panel{position:fixed;right:16px;bottom:16px;z-index:2147483646;width:280px;
    max-height:calc(100vh - 32px);display:flex;flex-direction:column;
    background:rgba(250,249,247,.78);color:#29261b;
    -webkit-backdrop-filter:blur(24px) saturate(160%);backdrop-filter:blur(24px) saturate(160%);
    border:.5px solid rgba(255,255,255,.6);border-radius:14px;
    box-shadow:0 1px 0 rgba(255,255,255,.5) inset,0 12px 40px rgba(0,0,0,.18);
    font:11.5px/1.4 ui-sans-serif,system-ui,-apple-system,sans-serif;overflow:hidden}
  [data-theme="dark"] .twk-panel{background:rgba(28,26,22,.78);color:#f3efe5;
    border-color:rgba(255,255,255,.08);
    box-shadow:0 1px 0 rgba(255,255,255,.05) inset,0 12px 40px rgba(0,0,0,.5)}
  .twk-hd{display:flex;align-items:center;justify-content:space-between;
    padding:10px 8px 10px 14px;cursor:move;user-select:none}
  .twk-hd b{font-size:12px;font-weight:600;letter-spacing:.01em}
  .twk-x{appearance:none;border:0;background:transparent;color:rgba(41,38,27,.55);
    width:22px;height:22px;border-radius:6px;cursor:default;font-size:13px;line-height:1}
  [data-theme="dark"] .twk-x{color:rgba(243,239,229,.55)}
  .twk-x:hover{background:rgba(0,0,0,.06);color:#29261b}
  [data-theme="dark"] .twk-x:hover{background:rgba(255,255,255,.08);color:#f3efe5}
  .twk-body{padding:2px 14px 14px;display:flex;flex-direction:column;gap:10px;
    overflow-y:auto;overflow-x:hidden;min-height:0;
    scrollbar-width:thin;scrollbar-color:rgba(0,0,0,.15) transparent}
  .twk-body::-webkit-scrollbar{width:8px}
  .twk-body::-webkit-scrollbar-track{background:transparent;margin:2px}
  .twk-body::-webkit-scrollbar-thumb{background:rgba(0,0,0,.15);border-radius:4px;
    border:2px solid transparent;background-clip:content-box}
  .twk-body::-webkit-scrollbar-thumb:hover{background:rgba(0,0,0,.25);
    border:2px solid transparent;background-clip:content-box}
  .twk-row{display:flex;flex-direction:column;gap:5px}
  .twk-row-h{flex-direction:row;align-items:center;justify-content:space-between;gap:10px}
  .twk-lbl{display:flex;justify-content:space-between;align-items:baseline;
    color:rgba(41,38,27,.72)}
  [data-theme="dark"] .twk-lbl{color:rgba(243,239,229,.72)}
  .twk-lbl>span:first-child{font-weight:500}

  .twk-sect{font-size:10px;font-weight:600;letter-spacing:.06em;text-transform:uppercase;
    color:rgba(41,38,27,.45);padding:10px 0 0}
  [data-theme="dark"] .twk-sect{color:rgba(243,239,229,.45)}
  .twk-sect:first-child{padding-top:0}

  .twk-seg{position:relative;display:flex;padding:2px;border-radius:8px;
    background:rgba(0,0,0,.06);user-select:none}
  [data-theme="dark"] .twk-seg{background:rgba(255,255,255,.08)}
  .twk-seg-thumb{position:absolute;top:2px;bottom:2px;border-radius:6px;
    background:rgba(255,255,255,.9);box-shadow:0 1px 2px rgba(0,0,0,.12);
    transition:left .15s cubic-bezier(.3,.7,.4,1),width .15s}
  [data-theme="dark"] .twk-seg-thumb{background:rgba(60,56,48,.95);
    box-shadow:0 1px 2px rgba(0,0,0,.35)}
  .twk-seg.dragging .twk-seg-thumb{transition:none}
  .twk-seg button{appearance:none;position:relative;z-index:1;flex:1;border:0;
    background:transparent;color:inherit;font:inherit;font-weight:500;min-height:22px;
    border-radius:6px;cursor:default;padding:4px 6px;line-height:1.2;
    overflow-wrap:anywhere}

  .twk-chips{display:flex;gap:6px}
  .twk-chip{position:relative;appearance:none;flex:1;min-width:0;height:46px;
    padding:0;border:0;border-radius:6px;overflow:hidden;cursor:default;
    box-shadow:0 0 0 .5px rgba(0,0,0,.12),0 1px 2px rgba(0,0,0,.06);
    transition:transform .12s cubic-bezier(.3,.7,.4,1),box-shadow .12s}
  .twk-chip:hover{transform:translateY(-1px);
    box-shadow:0 0 0 .5px rgba(0,0,0,.18),0 4px 10px rgba(0,0,0,.12)}
  .twk-chip[data-on="1"]{box-shadow:0 0 0 1.5px rgba(0,0,0,.85),
    0 2px 6px rgba(0,0,0,.15)}
  [data-theme="dark"] .twk-chip[data-on="1"]{box-shadow:0 0 0 1.5px rgba(255,255,255,.85),
    0 2px 6px rgba(0,0,0,.35)}
  .twk-chip svg{position:absolute;top:6px;left:6px;width:13px;height:13px;
    filter:drop-shadow(0 1px 1px rgba(0,0,0,.3))}
`;

// ─────────── State + persistence ───────────

export type Density = 'compact' | 'regular' | 'comfortable';
export type Theme = 'light' | 'dark';
export type Accent = 'emerald' | 'indigo' | 'copper' | 'slate';

export type Prefs = {
  density: Density;
  theme: Theme;
  accent: Accent;
};

const DEFAULT_PREFS: Prefs = {
  density: 'regular',
  theme: 'light',
  accent: 'emerald',
};

const STORAGE_KEY = 'nx.prefs.v1';

function loadPrefs(): Prefs {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULT_PREFS;
    const parsed = JSON.parse(raw) as Partial<Prefs>;
    return { ...DEFAULT_PREFS, ...parsed };
  } catch {
    return DEFAULT_PREFS;
  }
}

function savePrefs(p: Prefs) {
  try { localStorage.setItem(STORAGE_KEY, JSON.stringify(p)); } catch {}
}

/** Reads prefs once, applies them to documentElement, and exposes a setter
 *  that persists + reapplies. Initial application happens before paint so
 *  there's no flash of unstyled prefs. */
export function usePrefs() {
  const [prefs, setPrefs] = useState<Prefs>(() => loadPrefs());

  // Apply on mount + every change.
  useEffect(() => {
    const html = document.documentElement;
    html.setAttribute('data-theme', prefs.theme);
    html.setAttribute('data-density', prefs.density);
    html.setAttribute('data-accent', prefs.accent);
    savePrefs(prefs);
  }, [prefs]);

  const setPref = useCallback(
    <K extends keyof Prefs>(key: K, value: Prefs[K]) => {
      setPrefs((p) => ({ ...p, [key]: value }));
    },
    [],
  );

  return [prefs, setPref] as const;
}

// ─────────── Panel container ───────────

export function TweaksPanel({
  title = 'Preferences',
  defaultOpen = false,
  children,
}: {
  title?: string;
  defaultOpen?: boolean;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const dragRef = useRef<HTMLDivElement | null>(null);
  const offsetRef = useRef({ x: 16, y: 16 });
  const PAD = 16;

  const clampToViewport = useCallback(() => {
    const panel = dragRef.current;
    if (!panel) return;
    const w = panel.offsetWidth, h = panel.offsetHeight;
    const maxRight = Math.max(PAD, window.innerWidth - w - PAD);
    const maxBottom = Math.max(PAD, window.innerHeight - h - PAD);
    offsetRef.current = {
      x: Math.min(maxRight, Math.max(PAD, offsetRef.current.x)),
      y: Math.min(maxBottom, Math.max(PAD, offsetRef.current.y)),
    };
    panel.style.right = offsetRef.current.x + 'px';
    panel.style.bottom = offsetRef.current.y + 'px';
  }, []);

  useEffect(() => {
    if (!open) return;
    clampToViewport();
    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', clampToViewport);
      return () => window.removeEventListener('resize', clampToViewport);
    }
    const ro = new ResizeObserver(clampToViewport);
    ro.observe(document.documentElement);
    return () => ro.disconnect();
  }, [open, clampToViewport]);

  const onDragStart = (e: React.MouseEvent) => {
    const panel = dragRef.current;
    if (!panel) return;
    const r = panel.getBoundingClientRect();
    const sx = e.clientX, sy = e.clientY;
    const startRight = window.innerWidth - r.right;
    const startBottom = window.innerHeight - r.bottom;
    const move = (ev: MouseEvent) => {
      offsetRef.current = {
        x: startRight - (ev.clientX - sx),
        y: startBottom - (ev.clientY - sy),
      };
      clampToViewport();
    };
    const up = () => {
      window.removeEventListener('mousemove', move);
      window.removeEventListener('mouseup', up);
    };
    window.addEventListener('mousemove', move);
    window.addEventListener('mouseup', up);
  };

  return (
    <>
      <style>{PANEL_STYLE}</style>
      {!open && (
        <button className="twk-trigger" aria-label="Open preferences" onClick={() => setOpen(true)}>⚙︎</button>
      )}
      {open && (
        <div
          ref={dragRef}
          className="twk-panel"
          style={{ right: offsetRef.current.x, bottom: offsetRef.current.y }}
        >
          <div className="twk-hd" onMouseDown={onDragStart}>
            <b>{title}</b>
            <button
              className="twk-x"
              aria-label="Close preferences"
              onMouseDown={(e) => e.stopPropagation()}
              onClick={() => setOpen(false)}
            >✕</button>
          </div>
          <div className="twk-body">{children}</div>
        </div>
      )}
    </>
  );
}

// ─────────── Primitives ───────────

export function TweakSection({ label, children }: { label: ReactNode; children?: ReactNode }) {
  return (
    <>
      <div className="twk-sect">{label}</div>
      {children}
    </>
  );
}

export function TweakRow({
  label,
  value,
  children,
  inline = false,
}: {
  label: ReactNode;
  value?: ReactNode;
  children?: ReactNode;
  inline?: boolean;
}) {
  return (
    <div className={inline ? 'twk-row twk-row-h' : 'twk-row'}>
      <div className="twk-lbl">
        <span>{label}</span>
        {value != null && <span className="twk-val">{value}</span>}
      </div>
      {children}
    </div>
  );
}

type RadioOption<V> = V | { value: V; label: ReactNode };

function isLabeled<V>(o: RadioOption<V>): o is { value: V; label: ReactNode } {
  return typeof o === 'object' && o !== null && 'value' in (o as object);
}

export function TweakRadio<V extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: ReactNode;
  value: V;
  options: RadioOption<V>[];
  onChange: (v: V) => void;
}) {
  const trackRef = useRef<HTMLDivElement | null>(null);
  const [dragging, setDragging] = useState(false);
  const valueRef = useRef(value);
  valueRef.current = value;

  const opts = options.map((o) => (isLabeled(o) ? o : { value: o, label: String(o) }));
  const idx = Math.max(0, opts.findIndex((o) => o.value === value));
  const n = opts.length;

  const segAt = (clientX: number): V => {
    const track = trackRef.current!;
    const r = track.getBoundingClientRect();
    const inner = r.width - 4;
    const i = Math.floor(((clientX - r.left - 2) / inner) * n);
    return opts[Math.max(0, Math.min(n - 1, i))].value;
  };

  const onPointerDown = (e: React.PointerEvent) => {
    setDragging(true);
    const v0 = segAt(e.clientX);
    if (v0 !== valueRef.current) onChange(v0);
    const move = (ev: PointerEvent) => {
      if (!trackRef.current) return;
      const v = segAt(ev.clientX);
      if (v !== valueRef.current) onChange(v);
    };
    const up = () => {
      setDragging(false);
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  return (
    <TweakRow label={label}>
      <div
        ref={trackRef}
        role="radiogroup"
        onPointerDown={onPointerDown}
        className={dragging ? 'twk-seg dragging' : 'twk-seg'}
      >
        <div
          className="twk-seg-thumb"
          style={{
            left: `calc(2px + ${idx} * (100% - 4px) / ${n})`,
            width: `calc((100% - 4px) / ${n})`,
          }}
        />
        {opts.map((o) => (
          <button key={String(o.value)} type="button" role="radio" aria-checked={o.value === value}>
            {o.label}
          </button>
        ))}
      </div>
    </TweakRow>
  );
}

// ─────────── Color chips ───────────

function isLight(hex: string): boolean {
  const h = String(hex).replace('#', '');
  const x = h.length === 3 ? h.replace(/./g, (c) => c + c) : h.padEnd(6, '0');
  const n = parseInt(x.slice(0, 6), 16);
  if (Number.isNaN(n)) return true;
  const r = (n >> 16) & 255, g = (n >> 8) & 255, b = n & 255;
  return r * 299 + g * 587 + b * 114 > 148000;
}

const Check = ({ light }: { light: boolean }) => (
  <svg viewBox="0 0 14 14" aria-hidden="true">
    <path
      d="M3 7.2 5.8 10 11 4.2"
      fill="none"
      strokeWidth="2.2"
      strokeLinecap="round"
      strokeLinejoin="round"
      stroke={light ? 'rgba(0,0,0,.78)' : '#fff'}
    />
  </svg>
);

export function TweakColor({
  label,
  value,
  options,
  onChange,
}: {
  label: ReactNode;
  value: string;
  options: string[];
  onChange: (v: string) => void;
}) {
  return (
    <TweakRow label={label}>
      <div className="twk-chips" role="radiogroup">
        {options.map((hex) => {
          const on = hex.toLowerCase() === value.toLowerCase();
          return (
            <button
              key={hex}
              type="button"
              className="twk-chip"
              role="radio"
              aria-checked={on}
              data-on={on ? '1' : '0'}
              aria-label={hex}
              title={hex}
              style={{ background: hex }}
              onClick={() => onChange(hex)}
            >
              {on && <Check light={isLight(hex)} />}
            </button>
          );
        })}
      </div>
    </TweakRow>
  );
}

// ─────────── Prebuilt panel for nexusSacco ───────────

const ACCENT_HEX: Record<Accent, string> = {
  emerald: '#1F8A5B',
  indigo: '#4054C8',
  copper: '#BC6A33',
  slate: '#3A3F4B',
};
const HEX_TO_ACCENT: Record<string, Accent> = Object.fromEntries(
  Object.entries(ACCENT_HEX).map(([k, v]) => [v.toLowerCase(), k as Accent]),
) as Record<string, Accent>;

/** Drop-in component: mounts the panel + wires it to localStorage-backed prefs.
 *  Use directly in App.tsx; no props needed. */
export function Tweaks() {
  const [prefs, setPref] = usePrefs();

  return (
    <TweaksPanel title="nexusSacco · Preferences">
      <TweakSection label="Layout" />
      <TweakRadio<Density>
        label="Density"
        value={prefs.density}
        options={['compact', 'regular', 'comfortable']}
        onChange={(v) => setPref('density', v)}
      />

      <TweakSection label="Theme" />
      <TweakRadio<Theme>
        label="Mode"
        value={prefs.theme}
        options={['light', 'dark']}
        onChange={(v) => setPref('theme', v)}
      />
      <TweakColor
        label="Accent"
        value={ACCENT_HEX[prefs.accent]}
        options={Object.values(ACCENT_HEX)}
        onChange={(hex) => setPref('accent', HEX_TO_ACCENT[hex.toLowerCase()] ?? 'emerald')}
      />
    </TweaksPanel>
  );
}
