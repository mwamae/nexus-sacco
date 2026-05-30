// Phase-1 follow-up — public page reached from external comment SMS.
// Token in /m/c/{token} loads the thread (external comments only) and
// lets the member type a reply.

import { useEffect, useState, type FormEvent } from 'react';

type CommentRow = {
  id: string;
  visibility: 'internal' | 'external';
  body: string;
  author_name?: string;
  author_member_id?: string;
  posted_at: string;
};

function tokenFromPath(): string {
  const m = window.location.pathname.match(/^\/m\/c\/([^/]+)/);
  return m ? m[1] : '';
}

function apiPath(token: string, suffix = ''): string {
  return `/api/p/comments/${encodeURIComponent(token)}${suffix}`;
}

async function readJSON<T>(r: Response): Promise<T> {
  const text = await r.text();
  if (!r.ok) {
    let msg = `HTTP ${r.status}`;
    try { const j = JSON.parse(text); msg = j?.error?.message || j?.message || msg; } catch {}
    throw new Error(msg);
  }
  return text ? (JSON.parse(text) as T) : ({} as T);
}

export default function MemberCommentThread() {
  const token = tokenFromPath();
  const [items, setItems] = useState<CommentRow[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [reply, setReply] = useState('');
  const [busy, setBusy] = useState(false);

  async function load() {
    setErr(null);
    try {
      const r = await readJSON<{ items: CommentRow[] }>(await fetch(apiPath(token), { credentials: 'omit' }));
      setItems(r.items ?? []);
    } catch (e: any) {
      setErr(e?.message || 'Could not load thread.');
    }
  }
  useEffect(() => { void load(); }, [token]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!reply.trim()) return;
    setBusy(true); setErr(null);
    try {
      await readJSON(await fetch(apiPath(token, '/reply'), {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, credentials: 'omit',
        body: JSON.stringify({ body: reply.trim() }),
      }));
      setReply('');
      await load();
    } catch (e: any) {
      setErr(e?.message || 'Reply failed.');
    } finally { setBusy(false); }
  }

  return (
    <div className="auth-shell">
      <div className="auth-card" style={{ maxWidth: 520 }}>
        <div className="brand-mark">N</div>
        <div className="eyebrow">nexusSacco</div>
        <h1>Loan messages</h1>
        {err && <div className="alert alert-error">{err}</div>}
        {!items ? <div className="muted">Loading…</div> :
          items.length === 0 ? <div className="muted">No messages yet.</div> :
          <div style={{ marginTop: 10 }}>
            {items.map((c) => (
              <div key={c.id} style={{ padding: '10px 0', borderBottom: '1px solid var(--border, #eee)' }}>
                <div style={{ fontSize: 12, color: 'var(--muted, #888)' }}>
                  {c.author_member_id ? 'You' : (c.author_name || 'SACCO')} · {new Date(c.posted_at).toLocaleString()}
                </div>
                <div style={{ whiteSpace: 'pre-wrap', marginTop: 4 }}>{c.body}</div>
              </div>
            ))}
          </div>
        }
        <form onSubmit={submit} style={{ marginTop: 14 }}>
          <label className="form-label">Reply</label>
          <textarea className="form-control" rows={3} value={reply} onChange={(e) => setReply(e.target.value)} disabled={busy} placeholder="Type your reply…" />
          <button className="btn btn-primary btn-block" disabled={busy || !reply.trim()} style={{ marginTop: 8 }}>
            {busy ? 'Sending…' : 'Send reply'}
          </button>
        </form>
      </div>
    </div>
  );
}
