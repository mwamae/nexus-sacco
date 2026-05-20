// Roles & permissions admin. Lists system + tenant-custom roles, lets
// admins (with roles:edit) create / rename / delete custom roles and
// toggle permission checkboxes grouped by category.

import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  listRoles,
  listPermissions,
  createRole,
  updateRole,
  deleteRole,
  extractError,
  type ApiRole,
  type ApiPermission,
} from '../api/client';
import { Badge } from '../components/Badge';
import { Icon } from '../components/Icon';

type EditState =
  | { mode: 'closed' }
  | { mode: 'new' }
  | { mode: 'edit'; role: ApiRole };

export default function Roles() {
  const { hasPermission } = useAuth();
  const canEdit = hasPermission('roles:edit');

  const [roles, setRoles] = useState<ApiRole[] | null>(null);
  const [perms, setPerms] = useState<ApiPermission[] | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [edit, setEdit] = useState<EditState>({ mode: 'closed' });

  async function reload() {
    setLoadErr(null);
    try {
      const [r, p] = await Promise.all([listRoles(), listPermissions()]);
      setRoles(r);
      setPerms(p);
    } catch (e) {
      setLoadErr(extractError(e));
    }
  }
  useEffect(() => { void reload(); }, []);

  const permsByCategory = useMemo(() => {
    if (!perms) return {} as Record<string, ApiPermission[]>;
    const acc: Record<string, ApiPermission[]> = {};
    for (const p of perms) {
      (acc[p.category] ??= []).push(p);
    }
    return acc;
  }, [perms]);

  async function onDelete(role: ApiRole) {
    if (!confirm(`Delete role "${role.name}"? Users with only this role will lose access.`)) return;
    try {
      await deleteRole(role.id);
      await reload();
    } catch (e) {
      alert(extractError(e));
    }
  }

  const systemRoles = (roles ?? []).filter((r) => r.is_system);
  const customRoles = (roles ?? []).filter((r) => !r.is_system);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Administration · Access</div>
          <h1>Roles &amp; permissions</h1>
          <div className="page-sub">
            System roles are read-only. Create custom roles to grant a precise set of permissions.
          </div>
        </div>
        <div className="page-hd-actions">
          {canEdit && (
            <button className="btn btn-sm btn-accent" onClick={() => setEdit({ mode: 'new' })}>
              <Icon name="plus" size={13} /> New role
            </button>
          )}
        </div>
      </div>

      {loadErr && <div className="alert alert-error">{loadErr}</div>}

      {edit.mode !== 'closed' && perms && (
        <RoleForm
          permsByCategory={permsByCategory}
          existing={edit.mode === 'edit' ? edit.role : null}
          onClose={() => setEdit({ mode: 'closed' })}
          onSaved={async () => {
            setEdit({ mode: 'closed' });
            await reload();
          }}
        />
      )}

      <div className="grid-2" style={{ marginTop: 14 }}>
        <RoleTable
          title="Custom roles"
          subtitle="Defined for this tenant"
          rows={customRoles}
          empty="No custom roles yet."
          showActions={canEdit}
          onEdit={(r) => setEdit({ mode: 'edit', role: r })}
          onDelete={(r) => void onDelete(r)}
        />
        <RoleTable
          title="System roles"
          subtitle="Built-in templates · read-only"
          rows={systemRoles}
          empty="—"
          showActions={false}
        />
      </div>
    </div>
  );
}

function RoleTable({
  title,
  subtitle,
  rows,
  empty,
  showActions,
  onEdit,
  onDelete,
}: {
  title: string;
  subtitle?: string;
  rows: ApiRole[];
  empty: string;
  showActions: boolean;
  onEdit?: (r: ApiRole) => void;
  onDelete?: (r: ApiRole) => void;
}) {
  return (
    <div className="card">
      <div className="card-hd">
        <h3>{title}</h3>
        {subtitle && <span className="card-sub">{subtitle}</span>}
        <div className="card-hd-actions">
          <Badge tone="neutral">{rows.length}</Badge>
        </div>
      </div>
      <div className="card-body flush">
        {rows.length === 0 ? (
          <div className="empty">{empty}</div>
        ) : (
          <table className="tbl">
            <thead>
              <tr>
                <th>Role</th>
                <th>Permissions</th>
                {showActions && <th style={{ width: 1 }}></th>}
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id}>
                  <td>
                    <div style={{ fontWeight: 500 }}>{r.name}</div>
                    <div className="tiny-mono">{r.code}</div>
                    {r.description && <div className="muted tiny">{r.description}</div>}
                  </td>
                  <td className="tiny">
                    {(r.permissions || []).length === 0 ? (
                      <span className="muted">—</span>
                    ) : (
                      <span title={(r.permissions || []).join(', ')}>
                        <span className="mono">{(r.permissions || []).slice(0, 4).join(', ')}</span>
                        {(r.permissions || []).length > 4 && (
                          <span className="muted"> &nbsp;+{(r.permissions || []).length - 4}</span>
                        )}
                      </span>
                    )}
                  </td>
                  {showActions && (
                    <td className="al-c">
                      <div style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                        <button className="btn btn-sm" onClick={() => onEdit?.(r)}>
                          <Icon name="edit" size={12} /> Edit
                        </button>
                        <button className="btn btn-sm btn-ghost" style={{ color: 'var(--neg)' }} onClick={() => onDelete?.(r)}>
                          <Icon name="trash" size={12} />
                        </button>
                      </div>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function RoleForm({
  permsByCategory,
  existing,
  onClose,
  onSaved,
}: {
  permsByCategory: Record<string, ApiPermission[]>;
  existing: ApiRole | null;
  onClose: () => void;
  onSaved: () => void | Promise<void>;
}) {
  const [code, setCode] = useState(existing?.code ?? '');
  const [name, setName] = useState(existing?.name ?? '');
  const [description, setDescription] = useState(existing?.description ?? '');
  const [selected, setSelected] = useState<Set<string>>(new Set(existing?.permissions ?? []));
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function toggle(c: string) {
    setSelected((s) => {
      const next = new Set(s);
      if (next.has(c)) next.delete(c); else next.add(c);
      return next;
    });
  }
  function toggleCategory(category: string, on: boolean) {
    setSelected((s) => {
      const next = new Set(s);
      for (const p of permsByCategory[category] ?? []) {
        if (on) next.add(p.code); else next.delete(p.code);
      }
      return next;
    });
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setErr(null);
    setBusy(true);
    try {
      const permissions = Array.from(selected);
      if (existing) {
        await updateRole(existing.id, { name, description, permissions });
      } else {
        await createRole({ code, name, description, permissions });
      }
      await onSaved();
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  const categories = Object.keys(permsByCategory).sort();

  return (
    <div className="card" style={{ borderColor: 'var(--accent)' }}>
      <div className="card-hd">
        <h3>{existing ? `Edit · ${existing.code}` : 'New custom role'}</h3>
        <span className="card-sub">{selected.size} permission{selected.size === 1 ? '' : 's'} selected</span>
        <div className="card-hd-actions">
          <button className="btn btn-sm btn-ghost" onClick={onClose} aria-label="Close">
            <Icon name="x" size={13} />
          </button>
        </div>
      </div>
      <form onSubmit={submit} className="card-body">
        {err && <div className="alert alert-error">{err}</div>}

        <div className="grid-3">
          <div className="field">
            <label className="field-label">Code <span className="req">*</span></label>
            <input
              className="input mono"
              required
              minLength={3}
              maxLength={40}
              pattern="[a-z][a-z0-9_]{1,38}[a-z0-9]"
              value={code}
              disabled={!!existing}
              onChange={(e) => setCode(e.target.value.toLowerCase())}
              placeholder="loan_reviewer"
            />
            {!existing && <div className="field-hint">Lowercase letters/digits/underscores. Immutable after creation.</div>}
          </div>
          <div className="field">
            <label className="field-label">Name <span className="req">*</span></label>
            <input
              className="input"
              required
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Loan Reviewer"
            />
          </div>
          <div className="field">
            <label className="field-label">Description</label>
            <input
              className="input"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Read-only loan + member view"
            />
          </div>
        </div>

        <div className="divider" />
        <div className="h-sec">Permissions</div>
        <div className="grid-3">
          {categories.map((cat) => {
            const inCat = permsByCategory[cat];
            const allOn = inCat.every((p) => selected.has(p.code));
            const someOn = inCat.some((p) => selected.has(p.code));
            return (
              <div key={cat} className="card" style={{ background: 'var(--surface-2)' }}>
                <div className="card-hd">
                  <h3 style={{ textTransform: 'uppercase', fontSize: 11, letterSpacing: '.06em', color: 'var(--fg-3)' }}>
                    {cat}
                  </h3>
                  <span className="card-sub">{inCat.filter((p) => selected.has(p.code)).length}/{inCat.length}</span>
                  <div className="card-hd-actions">
                    <button
                      type="button"
                      className="btn btn-sm btn-ghost"
                      onClick={() => toggleCategory(cat, !allOn)}
                    >
                      {allOn ? 'clear' : someOn ? 'select rest' : 'select all'}
                    </button>
                  </div>
                </div>
                <div className="card-body" style={{ padding: '8px 12px' }}>
                  {inCat.map((p) => (
                    <label
                      key={p.code}
                      style={{
                        display: 'flex', gap: 8, alignItems: 'flex-start',
                        padding: '4px 0', cursor: 'pointer',
                      }}
                    >
                      <input
                        type="checkbox"
                        checked={selected.has(p.code)}
                        onChange={() => toggle(p.code)}
                        style={{ marginTop: 2, accentColor: 'var(--accent)' }}
                      />
                      <span style={{ flex: 1, minWidth: 0 }}>
                        <span className="mono tiny" style={{ color: 'var(--accent-fg)' }}>{p.code}</span>
                        <div className="muted tiny">{p.description}</div>
                      </span>
                    </label>
                  ))}
                </div>
              </div>
            );
          })}
        </div>

        <div className="row" style={{ marginTop: 16, gap: 8 }}>
          <button type="submit" className="btn btn-accent" disabled={busy}>
            <Icon name="check" size={13} />
            {busy ? 'Saving…' : existing ? 'Save changes' : 'Create role'}
          </button>
          <button type="button" className="btn btn-ghost" onClick={onClose} disabled={busy}>Cancel</button>
        </div>
      </form>
    </div>
  );
}
