// Template manager — Stage 8.
//
// Templates are stored per (tenant, event, channel). Admins can:
//   • Browse the catalogue grouped by event_code
//   • Edit a template's subject/body and toggle the active flag
//   • Clone an existing template (handy when staging a new version)
//   • Delete a template (use with care — events with no active template
//     are silently dropped on the destination channel at dispatch time)
//   • Live-preview a body with sample payload values to sanity-check
//     the {{variable}} substitution

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import {
  cloneNotificationTemplate,
  createNotificationTemplate,
  deleteNotificationTemplate,
  listNotificationEvents,
  listNotificationTemplates,
  previewNotificationTemplate,
  updateNotificationTemplate,
  type NotificationChannel,
  type NotificationEvent,
  type NotificationTemplate,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Icon } from '../components/Icon';

const CHANNELS: NotificationChannel[] = ['in_app', 'sms', 'email'];

export default function NotificationTemplatesPage() {
  const { tenant } = useAuth();
  const [events, setEvents] = useState<NotificationEvent[] | null>(null);
  const [templates, setTemplates] = useState<NotificationTemplate[] | null>(null);
  const [filter, setFilter] = useState('');
  const [editing, setEditing] = useState<NotificationTemplate | null>(null);
  const [creatingForEvent, setCreatingForEvent] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try {
      const [evs, tpls] = await Promise.all([
        listNotificationEvents(),
        listNotificationTemplates(),
      ]);
      setEvents(evs);
      setTemplates(tpls);
    } catch (e) {
      setErr(extractErr(e));
    }
  }
  useEffect(() => { void load(); }, []);

  const grouped = useMemo(() => {
    if (!events || !templates) return null;
    const byEvent = new Map<string, NotificationTemplate[]>();
    for (const t of templates) {
      const list = byEvent.get(t.event_code) ?? [];
      list.push(t);
      byEvent.set(t.event_code, list);
    }
    const q = filter.toLowerCase().trim();
    return events
      .filter((e) => !q || e.code.toLowerCase().includes(q) || e.description.toLowerCase().includes(q))
      .map((e) => ({
        event: e,
        templates: byEvent.get(e.code) ?? [],
      }));
  }, [events, templates, filter]);

  const eventByCode = useMemo(() => {
    const m = new Map<string, NotificationEvent>();
    for (const e of events ?? []) m.set(e.code, e);
    return m;
  }, [events]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Notification templates</div>
          <h1>Templates</h1>
          <div className="page-sub">
            Per-tenant copy for every event the platform emits. Edit the body to match your SACCO's voice.
          </div>
        </div>
        <div className="page-hd-actions">
          <button className="btn btn-ghost" onClick={() => void load()}>Refresh</button>
        </div>
      </div>

      <div className="card">
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
          <input
            type="search"
            placeholder="Filter events by code or description"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            style={{ width: 320 }}
          />
          <span className="muted tiny">
            {grouped?.length ?? 0} events · {templates?.length ?? 0} templates
          </span>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      {grouped === null && <div className="empty">Loading…</div>}
      {grouped !== null && grouped.length === 0 && (
        <div className="empty">No events match your filter.</div>
      )}

      {grouped?.map(({ event, templates: tpls }) => (
        <div key={event.code} className="card" style={{ marginTop: 12 }}>
          <div className="card-hd">
            <h3>
              <code style={{ fontFamily: 'var(--font-mono)' }}>{event.code}</code>
              <span className="muted tiny" style={{ marginLeft: 10, fontWeight: 'normal' }}>
                {event.category} · default priority {event.default_priority}
              </span>
            </h3>
            <div className="card-hd-actions">
              <button
                className="btn btn-sm btn-ghost"
                onClick={() => setCreatingForEvent(event.code)}
              >
                + Add template
              </button>
            </div>
          </div>
          <div className="card-body">
            {event.description && <p className="muted tiny" style={{ marginTop: 0 }}>{event.description}</p>}
            {event.allowed_variables && event.allowed_variables.length > 0 && (
              <p className="tiny" style={{ marginTop: 0 }}>
                <strong>Variables:</strong>{' '}
                {event.allowed_variables.map((v) => (
                  <code key={v} className="tiny" style={{ marginRight: 6, opacity: 0.8 }}>
                    {'{{'+v+'}}'}
                  </code>
                ))}
              </p>
            )}
            {tpls.length === 0 && (
              <div className="muted tiny" style={{ marginTop: 6 }}>
                No templates configured. Events without templates are silently dropped on the destination channel.
              </div>
            )}
            {tpls.length > 0 && (
              <table className="tbl" style={{ marginTop: 8 }}>
                <thead>
                  <tr>
                    <th style={{ width: 80 }}>Channel</th>
                    <th>Subject</th>
                    <th>Preview</th>
                    <th style={{ width: 80 }}>Active</th>
                    <th style={{ width: 180 }}></th>
                  </tr>
                </thead>
                <tbody>
                  {tpls.map((t) => (
                    <tr key={t.id}>
                      <td className="tiny">{t.channel}</td>
                      <td className="tiny">{t.subject ?? <span className="muted">—</span>}</td>
                      <td className="tiny" style={{ maxWidth: 380, whiteSpace: 'pre-wrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                        {t.body.slice(0, 110)}{t.body.length > 110 ? '…' : ''}
                      </td>
                      <td>
                        <span style={{ color: t.is_active ? 'var(--pos)' : 'var(--fg-3)', fontWeight: 600 }}>
                          {t.is_active ? 'On' : 'Off'}
                        </span>
                      </td>
                      <td>
                        <button className="btn btn-sm btn-ghost" onClick={() => setEditing(t)}>Edit</button>
                        <button
                          className="btn btn-sm btn-ghost"
                          onClick={async () => {
                            try { await cloneNotificationTemplate(t.id); await load(); }
                            catch (e) { setErr(extractErr(e)); }
                          }}
                        >
                          Clone
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>
      ))}

      {editing && (
        <EditorModal
          template={editing}
          event={eventByCode.get(editing.event_code) ?? null}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); void load(); }}
        />
      )}
      {creatingForEvent && (
        <EditorModal
          template={null}
          event={eventByCode.get(creatingForEvent) ?? null}
          onClose={() => setCreatingForEvent(null)}
          onSaved={() => { setCreatingForEvent(null); void load(); }}
        />
      )}
    </div>
  );
}

// ─────────── Editor modal ───────────

function EditorModal({
  template, event, onClose, onSaved,
}: {
  template: NotificationTemplate | null;
  event: NotificationEvent | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [eventCode, setEventCode] = useState(template?.event_code ?? event?.code ?? '');
  const [channel, setChannel] = useState<NotificationChannel>(template?.channel ?? 'in_app');
  const [subject, setSubject] = useState(template?.subject ?? '');
  const [body, setBody] = useState(template?.body ?? '');
  const [isActive, setIsActive] = useState(template?.is_active ?? true);
  const [previewSubj, setPreviewSubj] = useState('');
  const [previewBody, setPreviewBody] = useState('');
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  // Sample payload built from the event's allowed_variables. Each var
  // gets a placeholder string the admin can recognize in the preview.
  const samplePayload = useMemo(() => {
    const p: Record<string, string> = {};
    for (const v of event?.allowed_variables ?? []) {
      p[v] = `<${v}>`;
    }
    return p;
  }, [event]);

  useEffect(() => {
    let cancelled = false;
    if (!body.trim()) {
      setPreviewBody('');
      setPreviewSubj('');
      return;
    }
    const t = setTimeout(async () => {
      try {
        const r = await previewNotificationTemplate({ subject, body, payload: samplePayload });
        if (!cancelled) {
          setPreviewSubj(r.subject);
          setPreviewBody(r.body);
        }
      } catch {
        if (!cancelled) {
          setPreviewBody('(preview failed)');
        }
      }
    }, 250);
    return () => { cancelled = true; clearTimeout(t); };
  }, [subject, body, samplePayload]);

  function insertVar(v: string) {
    setBody((b) => b + (b.endsWith(' ') || b === '' ? '' : ' ') + `{{${v}}}`);
  }

  async function onSave() {
    setErr(null);
    setBusy('save');
    try {
      const payload = {
        event_code: eventCode,
        channel,
        subject: subject || undefined,
        body,
        is_active: isActive,
      };
      if (template) {
        await updateNotificationTemplate(template.id, payload);
      } else {
        await createNotificationTemplate(payload);
      }
      onSaved();
    } catch (e) {
      setErr(extractErr(e));
    } finally {
      setBusy(null);
    }
  }

  async function onDelete() {
    if (!template) return;
    if (!window.confirm(`Delete this ${template.channel} template for ${template.event_code}? This cannot be undone.`)) {
      return;
    }
    setErr(null);
    setBusy('delete');
    try {
      await deleteNotificationTemplate(template.id);
      onSaved();
    } catch (e) {
      setErr(extractErr(e));
    } finally {
      setBusy(null);
    }
  }

  const canSubmit = eventCode.trim() && body.trim();

  return (
    <Modal title={template ? 'Edit template' : `New template for ${event?.code ?? 'event'}`} onClose={onClose} width={760}>
      <div className="row" style={{ gap: 12, marginBottom: 10 }}>
        <div style={{ flex: 1 }}>
          <Field label="Event">
            <input value={eventCode} onChange={(e) => setEventCode(e.target.value)} style={{ width: '100%', fontFamily: 'var(--font-mono)' }} />
          </Field>
        </div>
        <div style={{ width: 140 }}>
          <Field label="Channel">
            <select value={channel} onChange={(e) => setChannel(e.target.value as NotificationChannel)} style={{ width: '100%' }}>
              {CHANNELS.map((c) => <option key={c} value={c}>{c}</option>)}
            </select>
          </Field>
        </div>
        <div style={{ width: 100 }}>
          <Field label="Active">
            <label className="row" style={{ gap: 4 }}>
              <input type="checkbox" checked={isActive} onChange={(e) => setIsActive(e.target.checked)} />
              On
            </label>
          </Field>
        </div>
      </div>

      {channel === 'email' && (
        <Field label="Subject (email only)">
          <input
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
            placeholder="e.g. Your loan {{loan_no}} was approved"
            style={{ width: '100%' }}
          />
        </Field>
      )}

      <Field label="Body" hint="Use {{variable}} placeholders. Click a variable below to insert it.">
        <textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          rows={6}
          style={{ width: '100%', fontFamily: 'var(--font-mono)', fontSize: 12 }}
        />
      </Field>

      {event?.allowed_variables && event.allowed_variables.length > 0 && (
        <div style={{ marginBottom: 12 }}>
          <div className="muted tiny" style={{ marginBottom: 4 }}>Available variables (click to insert)</div>
          <div className="row" style={{ gap: 4, flexWrap: 'wrap' }}>
            {event.allowed_variables.map((v) => (
              <button
                key={v}
                type="button"
                className="btn btn-sm btn-ghost"
                onClick={() => insertVar(v)}
                style={{ fontFamily: 'var(--font-mono)', fontSize: 11 }}
              >
                {'{{'+v+'}}'}
              </button>
            ))}
          </div>
        </div>
      )}

      <div className="card" style={{ marginTop: 8 }}>
        <div className="card-hd"><h3 style={{ margin: 0 }}>Live preview</h3></div>
        <div className="card-body">
          {previewSubj && <div style={{ marginBottom: 6 }}><strong className="tiny">Subject:</strong> {previewSubj}</div>}
          <div style={{ whiteSpace: 'pre-wrap', fontSize: 13 }}>{previewBody || <span className="muted tiny">enter body content to see preview…</span>}</div>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      <div className="row" style={{ gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
        {template && (
          <button className="btn btn-danger" onClick={() => void onDelete()} disabled={!!busy}>
            {busy === 'delete' ? 'Deleting…' : 'Delete'}
          </button>
        )}
        <button className="btn btn-ghost" onClick={onClose} disabled={!!busy}>Cancel</button>
        <button className="btn btn-accent" disabled={!canSubmit || !!busy} onClick={() => void onSave()}>
          {busy === 'save' ? 'Saving…' : template ? 'Save changes' : 'Create template'}
        </button>
      </div>
    </Modal>
  );
}

// ─────────── Tiny shared bits ───────────

function Modal({ title, children, onClose, width }: { title: string; children: ReactNode; onClose: () => void; width?: number }) {
  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: width ?? 560, maxWidth: '92vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3>{title}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body">{children}</div>
      </div>
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      {children}
      {hint && <div className="muted tiny" style={{ marginTop: 4 }}>{hint}</div>}
    </label>
  );
}

function extractErr(e: unknown): string {
  if (e && typeof e === 'object' && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'Unknown error';
}
