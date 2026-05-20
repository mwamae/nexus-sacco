// Workflow Definitions admin — list + create-new-version editor.
//
// Definitions are entity-agnostic; you bind one to a host module by
// matching `process_kind`. Editing a definition creates a new version
// and (if active=true) deactivates the previous live version.

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  createWorkflowDefinition,
  listWorkflowDefinitions,
  setWorkflowActivation,
  extractError,
  type CreateWFDefinitionInput,
  type WFDefinition,
  type WFLevelDef,
  type WFQuorum,
} from '../api/client';
import { Badge } from '../components/Badge';
import { Icon } from '../components/Icon';

// Convenience: a few sensible process kinds the wizard suggests.
// Hosts can use anything — these are just shortcuts.
const SUGGESTED_KINDS = [
  'member_onboarding', 'org_onboarding',
  'loan_disbursement', 'loan_restructure',
  'withdrawal', 'deposit_correction',
  'account_change', 'staff_invite',
];

const ROLE_OPTIONS = [
  'tenant_owner', 'sacco_admin', 'branch_manager', 'credit_officer',
  'teller', 'accountant', 'auditor', 'collections_officer',
];

type EditState = { mode: 'closed' } | { mode: 'new' } | { mode: 'new_version'; from: WFDefinition };

export default function WorkflowDefinitions() {
  const { hasPermission } = useAuth();
  const canEdit = hasPermission('workflow:configure');
  const [defs, setDefs] = useState<WFDefinition[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [edit, setEdit] = useState<EditState>({ mode: 'closed' });

  async function reload() {
    setErr(null);
    try { setDefs(await listWorkflowDefinitions()); }
    catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); }, []);

  const grouped = useMemo(() => {
    const m = new Map<string, WFDefinition[]>();
    for (const d of defs ?? []) {
      const k = d.process_kind;
      if (!m.has(k)) m.set(k, []);
      m.get(k)!.push(d);
    }
    // Sort versions desc within each kind.
    for (const arr of m.values()) arr.sort((a, b) => b.version - a.version);
    return m;
  }, [defs]);

  async function onToggle(d: WFDefinition) {
    try { await setWorkflowActivation(d.id, !d.active); await reload(); }
    catch (e) { alert(extractError(e)); }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Approvals · Configuration</div>
          <h1>Workflow definitions</h1>
          <div className="page-sub">One definition per process kind; editing creates a new version.</div>
        </div>
        <div className="page-hd-actions">
          {canEdit && (
            <button className="btn btn-sm btn-accent" onClick={() => setEdit({ mode: 'new' })}>
              <Icon name="plus" size={13} /> New workflow
            </button>
          )}
          <a className="btn btn-sm" href="/approvals">
            <Icon name="check" size={12} /> Inbox
          </a>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      {edit.mode !== 'closed' && (
        <Editor
          from={edit.mode === 'new_version' ? edit.from : null}
          onClose={() => setEdit({ mode: 'closed' })}
          onSaved={async () => { setEdit({ mode: 'closed' }); await reload(); }}
        />
      )}

      {!defs && !err && <div className="empty">Loading…</div>}
      {defs && defs.length === 0 && (
        <div className="empty">
          No workflow definitions yet.
          {canEdit && <> <a href="#" onClick={(e) => { e.preventDefault(); setEdit({ mode: 'new' }); }} style={{ color: 'var(--accent)' }}>Create the first →</a></>}
        </div>
      )}

      {[...grouped.entries()].map(([kind, versions]) => {
        const active = versions.find((v) => v.active) ?? versions[0];
        return (
          <div key={kind} className="card" style={{ marginBottom: 14 }}>
            <div className="card-hd">
              <h3>{kind.replace(/_/g, ' ')}</h3>
              <span className="card-sub">{versions.length} version{versions.length === 1 ? '' : 's'} · {active.levels.length} level{active.levels.length === 1 ? '' : 's'} in active</span>
              <div className="card-hd-actions">
                {canEdit && (
                  <button className="btn btn-sm" onClick={() => setEdit({ mode: 'new_version', from: active })}>
                    <Icon name="edit" size={12} /> New version
                  </button>
                )}
              </div>
            </div>
            <div className="card-body">
              <div className="row" style={{ gap: 6, flexWrap: 'wrap', marginBottom: 10 }}>
                {versions.map((v) => (
                  <button
                    key={v.id}
                    className={`fchip ${v.active ? '' : ''}`}
                    data-active={v.active || undefined}
                    onClick={() => canEdit && onToggle(v)}
                    title={canEdit ? 'Click to toggle active' : 'workflow:configure required'}
                    disabled={!canEdit}
                  >
                    v{v.version} {v.active && <Badge tone="pos">active</Badge>}
                  </button>
                ))}
              </div>
              <LevelSummary def={active} />
            </div>
          </div>
        );
      })}
    </div>
  );
}

function LevelSummary({ def }: { def: WFDefinition }) {
  return (
    <table className="tbl">
      <thead>
        <tr>
          <th style={{ width: 30 }}>#</th>
          <th>Level</th>
          <th>Approvers</th>
          <th>Quorum</th>
          <th>SLA</th>
          <th>Condition</th>
          <th>Escalation</th>
        </tr>
      </thead>
      <tbody>
        {def.levels.sort((a, b) => a.level_order - b.level_order).map((l) => (
          <tr key={l.level_order}>
            <td className="mono">{l.level_order + 1}</td>
            <td><strong>{l.name}</strong></td>
            <td className="tiny">
              {l.approver_roles.length > 0 ? l.approver_roles.map((r) => <Badge key={r} tone="neutral">{r}</Badge>) : <span className="muted">—</span>}
              {l.approver_user_ids.length > 0 && <span className="tiny-mono"> + {l.approver_user_ids.length} direct</span>}
            </td>
            <td>{l.quorum}</td>
            <td>{l.sla_hours ? `${l.sla_hours}h` : <span className="muted">—</span>}</td>
            <td className="tiny mono">{l.condition_expr ? JSON.stringify(l.condition_expr) : <span className="muted">always</span>}</td>
            <td className="tiny">{l.escalation_role || (l.escalation_user_id && 'direct user') || <span className="muted">—</span>}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// ─────────── Editor ───────────

type LevelDraft = {
  name: string;
  approver_roles: string[];
  approver_user_ids: string[];
  quorum: WFQuorum;
  sla_hours: string;
  condition_text: string; // JSON, validated on save
  escalation_role: string;
};

function emptyLevel(): LevelDraft {
  return {
    name: '', approver_roles: [], approver_user_ids: [],
    quorum: 'any_one', sla_hours: '', condition_text: '', escalation_role: '',
  };
}

function levelDraftFromDef(l: WFLevelDef): LevelDraft {
  return {
    name: l.name,
    approver_roles: [...l.approver_roles],
    approver_user_ids: [...l.approver_user_ids],
    quorum: l.quorum,
    sla_hours: l.sla_hours != null ? String(l.sla_hours) : '',
    condition_text: l.condition_expr ? JSON.stringify(l.condition_expr) : '',
    escalation_role: l.escalation_role ?? '',
  };
}

function Editor({
  from, onClose, onSaved,
}: { from: WFDefinition | null; onClose: () => void; onSaved: () => void | Promise<void> }) {
  const cloning = from != null;
  const [kind, setKind] = useState(from?.process_kind ?? '');
  const [name, setName] = useState(from?.name ?? '');
  const [description, setDescription] = useState(from?.description ?? '');
  const [active, setActive] = useState(true);
  const [levels, setLevels] = useState<LevelDraft[]>(() =>
    from ? from.levels.sort((a, b) => a.level_order - b.level_order).map(levelDraftFromDef) : [emptyLevel()]
  );
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function setL(i: number, patch: Partial<LevelDraft>) {
    setLevels((p) => {
      const next = p.slice();
      next[i] = { ...next[i], ...patch };
      return next;
    });
  }
  function addLevel() { setLevels((p) => [...p, emptyLevel()]); }
  function removeLevel(i: number) { setLevels((p) => p.filter((_, idx) => idx !== i)); }
  function moveLevel(i: number, dir: -1 | 1) {
    const j = i + dir;
    if (j < 0 || j >= levels.length) return;
    setLevels((p) => {
      const next = p.slice();
      [next[i], next[j]] = [next[j], next[i]];
      return next;
    });
  }
  function toggleRole(i: number, role: string) {
    setLevels((p) => {
      const next = p.slice();
      const arr = next[i].approver_roles;
      next[i] = { ...next[i], approver_roles: arr.includes(role) ? arr.filter((r) => r !== role) : [...arr, role] };
      return next;
    });
  }

  async function submit() {
    setErr(null);
    if (!kind.trim() || !name.trim()) { setErr('process_kind and name are required'); return; }
    if (levels.length === 0) { setErr('add at least one level'); return; }
    const parsed: CreateWFDefinitionInput['levels'] = [];
    for (let i = 0; i < levels.length; i++) {
      const l = levels[i];
      if (!l.name.trim()) { setErr(`level ${i + 1}: name is required`); return; }
      if (l.approver_roles.length === 0 && l.approver_user_ids.length === 0) {
        setErr(`level ${i + 1}: pick at least one approver role or direct user`); return;
      }
      let cond: unknown = undefined;
      if (l.condition_text.trim()) {
        try { cond = JSON.parse(l.condition_text); }
        catch (e) { setErr(`level ${i + 1}: condition JSON invalid — ${(e as Error).message}`); return; }
      }
      parsed.push({
        name: l.name.trim(),
        approver_roles: l.approver_roles,
        approver_user_ids: l.approver_user_ids,
        quorum: l.quorum,
        sla_hours: l.sla_hours ? Number(l.sla_hours) : undefined,
        condition_expr: cond,
        escalation_role: l.escalation_role || undefined,
      });
    }
    setBusy(true);
    try {
      await createWorkflowDefinition({
        process_kind: kind.trim(), name: name.trim(), description: description.trim() || undefined,
        active, levels: parsed,
      });
      await onSaved();
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card" style={{ borderColor: 'var(--accent)', marginBottom: 14 }}>
      <div className="card-hd">
        <h3>{cloning ? `New version of ${from!.process_kind}` : 'New workflow'}</h3>
        <span className="card-sub">{cloning ? `from v${from!.version}` : 'configurable level-by-level approval'}</span>
        <div className="card-hd-actions">
          <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={13} /></button>
        </div>
      </div>
      <div className="card-body">
        {err && <div className="alert alert-error">{err}</div>}

        <div className="grid-3">
          <Field label="Process kind" required hint="The host module uses this string to bind. Choose one or type a new one.">
            <input
              className="input mono"
              list="wf-kinds"
              value={kind}
              disabled={cloning}
              onChange={(e) => setKind(e.target.value.toLowerCase().replace(/\s+/g, '_'))}
              placeholder="loan_disbursement"
            />
            <datalist id="wf-kinds">
              {SUGGESTED_KINDS.map((k) => <option key={k} value={k} />)}
            </datalist>
          </Field>
          <Field label="Display name" required>
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Loan Disbursement (default)" />
          </Field>
          <Field label="Active on save">
            <label className="row" style={{ gap: 6 }}>
              <input type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} style={{ accentColor: 'var(--accent)' }} />
              <span className="tiny">Replace the live version (recommended)</span>
            </label>
          </Field>
          <Field label="Description" wide>
            <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What this workflow is for" />
          </Field>
        </div>

        <div className="divider" />
        <div className="h-sec">Levels (sequential top → bottom)</div>

        {levels.map((l, i) => (
          <div key={i} className="card" style={{ background: 'var(--surface-2)', marginBottom: 8 }}>
            <div className="card-hd">
              <h3>Level {i + 1}{l.name && <span className="muted tiny"> · {l.name}</span>}</h3>
              <div className="card-hd-actions">
                <button type="button" className="btn btn-sm btn-ghost" disabled={i === 0} onClick={() => moveLevel(i, -1)}>↑</button>
                <button type="button" className="btn btn-sm btn-ghost" disabled={i === levels.length - 1} onClick={() => moveLevel(i, 1)}>↓</button>
                {levels.length > 1 && (
                  <button type="button" className="btn btn-sm btn-ghost" style={{ color: 'var(--neg)' }} onClick={() => removeLevel(i)}>
                    <Icon name="trash" size={12} />
                  </button>
                )}
              </div>
            </div>
            <div className="card-body">
              <div className="grid-3">
                <Field label="Name" required>
                  <input className="input" value={l.name} onChange={(e) => setL(i, { name: e.target.value })} placeholder="Maker / Checker / Approver" />
                </Field>
                <Field label="Quorum">
                  <select className="select" value={l.quorum} onChange={(e) => setL(i, { quorum: e.target.value as WFQuorum })}>
                    <option value="any_one">Any one approver</option>
                    <option value="all">All required (use direct users)</option>
                  </select>
                </Field>
                <Field label="SLA (hours)" hint="Optional. Blank = no SLA.">
                  <input className="input mono" type="number" min={0} value={l.sla_hours} onChange={(e) => setL(i, { sla_hours: e.target.value })} placeholder="24" />
                </Field>
              </div>

              <div className="field" style={{ marginTop: 8 }}>
                <label className="field-label">Approver roles</label>
                <div className="fchips">
                  {ROLE_OPTIONS.map((r) => (
                    <button
                      key={r}
                      type="button"
                      className="fchip"
                      data-active={l.approver_roles.includes(r) || undefined}
                      onClick={() => toggleRole(i, r)}
                    >
                      {r}
                    </button>
                  ))}
                </div>
                <div className="field-hint">Anyone with one of these roles can act unless overridden by direct users.</div>
              </div>

              <div className="grid-2">
                <Field label="Direct user IDs" hint="Comma-separated UUIDs. Useful for named approvers / quorum=all.">
                  <input
                    className="input mono"
                    value={l.approver_user_ids.join(', ')}
                    onChange={(e) => setL(i, { approver_user_ids: e.target.value.split(',').map((s) => s.trim()).filter(Boolean) })}
                    placeholder="3a7dec5c-…, 5d2bf123-…"
                  />
                </Field>
                <Field label="Escalation role" hint="Where to escalate when SLA breaches (auto-cron deferred).">
                  <select className="select" value={l.escalation_role} onChange={(e) => setL(i, { escalation_role: e.target.value })}>
                    <option value="">— none —</option>
                    {ROLE_OPTIONS.map((r) => <option key={r} value={r}>{r}</option>)}
                  </select>
                </Field>
              </div>

              <Field label='Condition (JSON, optional)' hint='Skip this level when the expression is false. e.g. {">":[{"var":"amount"},500000]}' wide>
                <textarea
                  className="input mono"
                  style={{ minHeight: 56, padding: 8, fontFamily: 'inherit', resize: 'vertical' }}
                  value={l.condition_text}
                  onChange={(e) => setL(i, { condition_text: e.target.value })}
                  placeholder='{">":[{"var":"amount"},500000]}'
                />
              </Field>
            </div>
          </div>
        ))}

        <button type="button" className="btn btn-sm" onClick={addLevel}>
          <Icon name="plus" size={12} /> Add level
        </button>

        <div className="row" style={{ marginTop: 16, gap: 8 }}>
          <button type="button" className="btn btn-accent btn-sm" disabled={busy} onClick={() => void submit()}>
            <Icon name="check" size={12} /> {busy ? 'Saving…' : cloning ? 'Save as new version' : 'Create workflow'}
          </button>
          <button type="button" className="btn btn-ghost btn-sm" disabled={busy} onClick={onClose}>Cancel</button>
        </div>
      </div>
    </div>
  );
}

function Field({ label, hint, required, wide, children }: { label: ReactNode; hint?: string; required?: boolean; wide?: boolean; children: ReactNode }) {
  return (
    <div className="field" style={wide ? { gridColumn: 'span 3' } : undefined}>
      <label className="field-label">
        {label}
        {required && <span className="req"> *</span>}
      </label>
      {children}
      {hint && <div className="field-hint">{hint}</div>}
    </div>
  );
}
