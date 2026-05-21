// Campaign manager — list, create, preview, schedule/send, cancel.
//
// Audience selector supports the 5 filter types the backend accepts:
//   • all members
//   • by status (active / dormant / suspended)
//   • members with active loans
//   • loan defaulters (DPD ≥ N)
//   • custom list (paste/select member IDs)
//
// The maker/checker threshold lives at the bottom of the page (admin
// changes propagate to all future campaigns).

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import {
  cancelCampaign,
  createCampaign,
  getCampaignSettings,
  listCampaigns,
  listNotificationTemplates,
  previewCampaign,
  scheduleCampaign,
  sendCampaign,
  updateCampaignSettings,
  type AudienceFilter,
  type Campaign,
  type CampaignPreview,
  type CampaignSettings,
  type CampaignStatus,
  type NotificationChannel,
  type NotificationTemplate,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Icon } from '../components/Icon';

const STATUS_LABELS: Record<CampaignStatus, string> = {
  draft: 'Draft',
  awaiting_approval: 'Awaiting approval',
  scheduled: 'Scheduled',
  sending: 'Sending',
  sent: 'Sent',
  cancelled: 'Cancelled',
  failed: 'Failed',
};

const STATUS_COLOR: Record<CampaignStatus, string> = {
  draft: 'var(--fg-3)',
  awaiting_approval: 'var(--warn)',
  scheduled: 'var(--accent)',
  sending: 'var(--accent)',
  sent: 'var(--pos)',
  cancelled: 'var(--fg-3)',
  failed: 'var(--neg)',
};

export default function CampaignsPage() {
  const { tenant } = useAuth();
  const [items, setItems] = useState<Campaign[] | null>(null);
  const [filterStatus, setFilterStatus] = useState<string>('');
  const [showCreate, setShowCreate] = useState(false);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try {
      const r = await listCampaigns({ status: filterStatus || undefined, limit: 100 });
      setItems(r.items);
    } catch (e) {
      setErr(extractErr(e));
    }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [filterStatus]);

  const selected = useMemo(() => items?.find((c) => c.id === selectedId) ?? null, [items, selectedId]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Campaigns</div>
          <h1>Campaigns</h1>
          <div className="page-sub">Send a notification to many members at once. Audience is resolved at dispatch time.</div>
        </div>
        <div className="page-hd-actions">
          <button className="btn btn-primary" onClick={() => setShowCreate(true)}>New campaign</button>
        </div>
      </div>

      <div className="card">
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
          <label className="muted tiny">Status</label>
          <select value={filterStatus} onChange={(e) => setFilterStatus(e.target.value)}>
            <option value="">All</option>
            {Object.entries(STATUS_LABELS).map(([k, v]) => (
              <option key={k} value={k}>{v}</option>
            ))}
          </select>
          <button className="btn btn-sm btn-ghost" onClick={() => void load()}>Refresh</button>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-body flush">
          {items === null && <div className="empty">Loading…</div>}
          {items !== null && items.length === 0 && (
            <div className="empty">No campaigns yet — click "New campaign" above to create one.</div>
          )}
          {items !== null && items.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Event</th>
                  <th>Channels</th>
                  <th className="num">Recipients</th>
                  <th>Progress</th>
                  <th>Status</th>
                  <th>Scheduled</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {items.map((c) => (
                  <tr key={c.id}>
                    <td><strong>{c.name}</strong></td>
                    <td><code className="tiny">{c.event_code}</code></td>
                    <td className="tiny">{c.channels.join(', ')}</td>
                    <td className="num">{c.estimated_recipients}</td>
                    <td className="tiny">
                      {c.total_recipients > 0
                        ? `${c.dispatched_count}/${c.total_recipients} sent${c.failed_count ? ` · ${c.failed_count} failed` : ''}`
                        : '—'}
                    </td>
                    <td>
                      <span style={{ color: STATUS_COLOR[c.status], fontWeight: 600 }}>
                        {STATUS_LABELS[c.status]}
                      </span>
                    </td>
                    <td className="tiny">{c.scheduled_for ? new Date(c.scheduled_for).toLocaleString() : '—'}</td>
                    <td>
                      <button className="btn btn-sm btn-ghost" onClick={() => setSelectedId(c.id)}>Open</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div style={{ marginTop: 16 }}>
        <SettingsSection />
      </div>

      {showCreate && (
        <CreateCampaignModal
          onClose={() => setShowCreate(false)}
          onCreated={(id) => {
            setShowCreate(false);
            void load();
            setSelectedId(id);
          }}
        />
      )}

      {selected && (
        <CampaignDetailModal
          campaign={selected}
          onClose={() => setSelectedId(null)}
          onChanged={() => void load()}
        />
      )}
    </div>
  );
}

// ─────────── Create ───────────

const CHANNELS: NotificationChannel[] = ['in_app', 'sms', 'email'];

function CreateCampaignModal({ onClose, onCreated }: { onClose: () => void; onCreated: (id: string) => void }) {
  const [name, setName] = useState('');
  const [eventCode, setEventCode] = useState('');
  const [channels, setChannels] = useState<NotificationChannel[]>(['in_app']);
  const [audience, setAudience] = useState<AudienceFilter>({ type: 'all_members' });
  const [scheduledFor, setScheduledFor] = useState('');
  const [payloadText, setPayloadText] = useState('{}');
  const [templates, setTemplates] = useState<NotificationTemplate[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    void (async () => {
      try {
        const t = await listNotificationTemplates();
        setTemplates(t);
      } catch {
        setTemplates([]);
      }
    })();
  }, []);

  const eventOptions = useMemo(() => {
    if (!templates) return [];
    const codes = new Set<string>();
    for (const t of templates) {
      if (t.is_active) codes.add(t.event_code);
    }
    return Array.from(codes).sort();
  }, [templates]);

  const availableChannels = useMemo(() => {
    if (!templates || !eventCode) return CHANNELS;
    const set = new Set<NotificationChannel>();
    for (const t of templates) {
      if (t.event_code === eventCode && t.is_active) set.add(t.channel);
    }
    return CHANNELS.filter((c) => set.has(c));
  }, [templates, eventCode]);

  async function onSubmit() {
    setErr(null);
    let payload: Record<string, unknown> = {};
    if (payloadText.trim()) {
      try {
        payload = JSON.parse(payloadText);
      } catch {
        setErr('Payload must be valid JSON');
        return;
      }
    }
    setBusy(true);
    try {
      const c = await createCampaign({
        name,
        event_code: eventCode,
        channels,
        audience,
        payload,
        // Send the picker's naive local string verbatim. The backend
        // interprets it in the tenant's configured timezone so "9am"
        // means 9am in the SACCO's local time regardless of where the
        // admin's browser is.
        scheduled_for: scheduledFor || undefined,
      });
      onCreated(c.id);
    } catch (e) {
      setErr(extractErr(e));
    } finally {
      setBusy(false);
    }
  }

  const canSubmit = !!name.trim() && !!eventCode && channels.length > 0 && audienceValid(audience);

  return (
    <ModalShell title="New campaign" onClose={onClose} onSubmit={onSubmit} submitLabel="Create campaign" busy={busy} disabled={!canSubmit} width={600}>
      <Field label="Name">
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. June dividend announcement" style={{ width: '100%' }} />
      </Field>

      <Field label="Event" hint="Only events with an active template are listed.">
        <select value={eventCode} onChange={(e) => setEventCode(e.target.value)} style={{ width: '100%' }}>
          <option value="">Choose an event…</option>
          {eventOptions.map((c) => <option key={c} value={c}>{c}</option>)}
        </select>
      </Field>

      <Field label="Channels">
        <div className="row" style={{ gap: 10, flexWrap: 'wrap' }}>
          {CHANNELS.map((ch) => {
            const enabled = !eventCode || availableChannels.includes(ch);
            const checked = channels.includes(ch);
            return (
              <label key={ch} className="row" style={{ gap: 4, alignItems: 'center', opacity: enabled ? 1 : 0.4 }}>
                <input
                  type="checkbox"
                  checked={checked}
                  disabled={!enabled}
                  onChange={(e) => {
                    if (e.target.checked) setChannels([...channels.filter((c) => c !== ch), ch]);
                    else setChannels(channels.filter((c) => c !== ch));
                  }}
                />
                {ch}
              </label>
            );
          })}
        </div>
      </Field>

      <Field label="Audience">
        <AudienceEditor value={audience} onChange={setAudience} />
      </Field>

      <Field label="Payload (JSON, optional)" hint="Merged with per-member vars (member_no, full_name, recipient_name) at dispatch.">
        <textarea
          value={payloadText}
          onChange={(e) => setPayloadText(e.target.value)}
          rows={4}
          placeholder='{"message":"Your statement is ready"}'
          style={{ width: '100%', fontFamily: 'monospace', fontSize: 12 }}
        />
      </Field>

      <Field label="Schedule for (optional)" hint="Leave empty to create as draft. You can schedule or send from the detail view.">
        <input
          type="datetime-local"
          value={scheduledFor}
          onChange={(e) => setScheduledFor(e.target.value)}
        />
      </Field>

      {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
    </ModalShell>
  );
}

// ─────────── Audience editor ───────────

function audienceValid(a: AudienceFilter): boolean {
  switch (a.type) {
    case 'status':
      return !!a.status;
    case 'custom_list':
      return a.member_ids.length > 0;
    default:
      return true;
  }
}

function AudienceEditor({ value, onChange }: { value: AudienceFilter; onChange: (v: AudienceFilter) => void }) {
  return (
    <>
      <select value={value.type} onChange={(e) => {
        const t = e.target.value as AudienceFilter['type'];
        switch (t) {
          case 'all_members': onChange({ type: 'all_members' }); break;
          case 'status': onChange({ type: 'status', status: 'active' }); break;
          case 'active_loans': onChange({ type: 'active_loans' }); break;
          case 'loan_defaulters': onChange({ type: 'loan_defaulters', dpd_min: 30 }); break;
          case 'custom_list': onChange({ type: 'custom_list', member_ids: [] }); break;
        }
      }} style={{ width: '100%' }}>
        <option value="all_members">All members</option>
        <option value="status">By status</option>
        <option value="active_loans">Members with active loans</option>
        <option value="loan_defaulters">Loan defaulters (by DPD)</option>
        <option value="custom_list">Custom list of member IDs</option>
      </select>

      {value.type === 'status' && (
        <div style={{ marginTop: 8 }}>
          <select
            value={value.status}
            onChange={(e) => onChange({ type: 'status', status: e.target.value as 'active' | 'dormant' | 'suspended' })}
          >
            <option value="active">Active</option>
            <option value="dormant">Dormant</option>
            <option value="suspended">Suspended</option>
          </select>
        </div>
      )}

      {value.type === 'loan_defaulters' && (
        <div style={{ marginTop: 8 }}>
          <label className="muted tiny" style={{ marginRight: 6 }}>Minimum DPD</label>
          <input
            type="number"
            min={1}
            value={value.dpd_min ?? 30}
            onChange={(e) => onChange({ type: 'loan_defaulters', dpd_min: Number(e.target.value) || 1 })}
            style={{ width: 90 }}
          />
        </div>
      )}

      {value.type === 'custom_list' && (
        <div style={{ marginTop: 8 }}>
          <textarea
            rows={3}
            placeholder="Paste UUIDs, one per line"
            value={value.member_ids.join('\n')}
            onChange={(e) => onChange({
              type: 'custom_list',
              member_ids: e.target.value.split(/\s+/).map((s) => s.trim()).filter(Boolean),
            })}
            style={{ width: '100%', fontFamily: 'monospace', fontSize: 12 }}
          />
          <div className="muted tiny">{value.member_ids.length} member IDs</div>
        </div>
      )}
    </>
  );
}

// ─────────── Detail ───────────

function CampaignDetailModal({
  campaign, onClose, onChanged,
}: {
  campaign: Campaign;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [preview, setPreview] = useState<CampaignPreview | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    void (async () => {
      try {
        const p = await previewCampaign(campaign.id);
        setPreview(p);
      } catch (e) {
        setErr(extractErr(e));
      }
    })();
  }, [campaign.id]);

  async function run<T>(label: string, fn: () => Promise<T>) {
    setErr(null);
    setBusy(label);
    try {
      await fn();
      onChanged();
    } catch (e) {
      setErr(extractErr(e));
    } finally {
      setBusy(null);
    }
  }

  const canMutate = (['draft', 'awaiting_approval', 'scheduled'] as CampaignStatus[]).includes(campaign.status);

  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: 720, maxWidth: '92vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3>{campaign.name}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body">
          <div className="row" style={{ gap: 24, flexWrap: 'wrap' }}>
            <Stat label="Status" value={STATUS_LABELS[campaign.status]} color={STATUS_COLOR[campaign.status]} />
            <Stat label="Event" value={campaign.event_code} mono />
            <Stat label="Channels" value={campaign.channels.join(', ')} />
            <Stat label="Est. recipients" value={String(campaign.estimated_recipients)} />
            {campaign.total_recipients > 0 && (
              <Stat label="Dispatched" value={`${campaign.dispatched_count}/${campaign.total_recipients}`} />
            )}
            {campaign.failed_count > 0 && (
              <Stat label="Failed" value={String(campaign.failed_count)} color="var(--neg)" />
            )}
            {campaign.scheduled_for && (
              <Stat label="Scheduled for" value={new Date(campaign.scheduled_for).toLocaleString()} />
            )}
            {campaign.sent_at && (
              <Stat label="Sent at" value={new Date(campaign.sent_at).toLocaleString()} />
            )}
          </div>

          {campaign.failure_reason && (
            <div className="alert alert-error" style={{ marginTop: 12 }}>
              <strong>Failure:</strong> {campaign.failure_reason}
            </div>
          )}
          {campaign.cancel_reason && (
            <div className="muted tiny" style={{ marginTop: 12 }}>
              Cancelled: {campaign.cancel_reason}
            </div>
          )}

          <h4 style={{ marginTop: 20, marginBottom: 8 }}>Preview</h4>
          {preview === null && !err && <div className="muted tiny">Loading preview…</div>}
          {preview && preview.samples && preview.samples.length > 0 && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              {preview.samples.map((s, i) => (
                <div key={i} className="card" style={{ padding: 10 }}>
                  <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
                    <strong className="tiny">{s.channel.toUpperCase()}</strong>
                    {s.subject && <span className="muted tiny">{s.subject}</span>}
                  </div>
                  <div style={{ whiteSpace: 'pre-wrap', marginTop: 6 }}>{s.body}</div>
                </div>
              ))}
            </div>
          )}
          {preview && (!preview.samples || preview.samples.length === 0) && (
            <div className="muted tiny">No template found for the selected event + channel combo.</div>
          )}

          {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}
        </div>
        <div className="card-body" style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', borderTop: '1px solid var(--border)' }}>
          {canMutate && (
            <>
              <button
                className="btn"
                disabled={!!busy}
                onClick={() => {
                  const reason = window.prompt('Reason for cancellation (optional)') ?? '';
                  void run('cancel', () => cancelCampaign(campaign.id, reason));
                }}
              >
                {busy === 'cancel' ? 'Cancelling…' : 'Cancel'}
              </button>
              <button
                className="btn"
                disabled={!!busy}
                onClick={() => {
                  const at = window.prompt('Schedule for (YYYY-MM-DD HH:mm, tenant local time)');
                  if (!at) return;
                  // Send the string as-is — backend resolves against
                  // the tenant's configured timezone, not the browser's.
                  const normalized = at.trim().replace(' ', 'T');
                  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/.test(normalized)) {
                    setErr('Invalid format — use YYYY-MM-DD HH:mm');
                    return;
                  }
                  void run('schedule', () => scheduleCampaign(campaign.id, normalized));
                }}
              >
                {busy === 'schedule' ? 'Scheduling…' : 'Schedule…'}
              </button>
              <button
                className="btn btn-accent"
                disabled={!!busy}
                onClick={() => {
                  if (!window.confirm(`Send ${campaign.estimated_recipients} notifications now?`)) return;
                  void run('send', () => sendCampaign(campaign.id));
                }}
              >
                {busy === 'send' ? 'Sending…' : `Send now (${campaign.estimated_recipients})`}
              </button>
            </>
          )}
          <button className="btn btn-ghost" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  );
}

// ─────────── Settings ───────────

function SettingsSection() {
  const [s, setS] = useState<CampaignSettings | null>(null);
  const [val, setVal] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<string | null>(null);

  useEffect(() => {
    void (async () => {
      try {
        const c = await getCampaignSettings();
        setS(c);
        setVal(String(c.approval_recipient_threshold));
      } catch (e) {
        setErr(extractErr(e));
      }
    })();
  }, []);

  async function onSave() {
    setErr(null);
    const n = Number(val);
    if (!Number.isFinite(n) || n < 0) {
      setErr('Threshold must be a positive number');
      return;
    }
    setBusy(true);
    try {
      const r = await updateCampaignSettings(n);
      setS(r);
      setSavedAt(new Date().toLocaleTimeString());
    } catch (e) {
      setErr(extractErr(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <div className="card-hd">
        <h3>Maker/checker approval threshold</h3>
      </div>
      <div className="card-body">
        <p className="muted tiny" style={{ marginTop: 0, marginBottom: 12 }}>
          Campaigns targeting more than this many recipients will require a second-staff approval before they
          can be sent. Set to 0 to require approval for every campaign.
        </p>
        {s === null && !err && <div className="muted tiny">Loading…</div>}
        {s !== null && (
          <div className="row" style={{ gap: 8, alignItems: 'center' }}>
            <input
              type="number"
              min={0}
              value={val}
              onChange={(e) => setVal(e.target.value)}
              style={{ width: 120 }}
            />
            <button className="btn btn-primary" onClick={() => void onSave()} disabled={busy}>
              {busy ? 'Saving…' : 'Save'}
            </button>
            {savedAt && <span className="muted tiny">Saved at {savedAt}</span>}
          </div>
        )}
        {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
      </div>
    </div>
  );
}

// ─────────── Tiny shared bits ───────────

function ModalShell({
  title, busy, disabled, onClose, onSubmit, submitLabel, children, width,
}: {
  title: string; busy?: boolean; disabled?: boolean; onClose: () => void;
  onSubmit: () => void | Promise<void>; submitLabel: string;
  children: ReactNode; width?: number;
}) {
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
        <div className="card-body" style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', borderTop: '1px solid var(--border)' }}>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-accent" disabled={busy || disabled} onClick={() => void onSubmit()}>{busy ? 'Working…' : submitLabel}</button>
        </div>
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

function Stat({ label, value, color, mono }: { label: string; value: string; color?: string; mono?: boolean }) {
  return (
    <div>
      <div className="muted tiny" style={{ marginBottom: 2 }}>{label}</div>
      <div style={{ fontWeight: 600, color, fontFamily: mono ? 'var(--font-mono)' : undefined }}>{value}</div>
    </div>
  );
}

function extractErr(e: unknown): string {
  if (e && typeof e === 'object' && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'Unknown error';
}
