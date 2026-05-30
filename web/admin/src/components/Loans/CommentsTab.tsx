// Phase-1 follow-up — Comments tab on the loan application + loan
// detail pages. Hybrid model: internal officer-only notes + external
// member-facing messages with SMS notification on post.

import { useEffect, useState } from 'react';
import {
  listApplicationComments,
  listLoanComments,
  postApplicationComment,
  postLoanComment,
  editLoanComment,
  pinLoanComment,
  deleteLoanComment,
  listLoanCommentTemplates,
  searchLoanComments,
  type LoanCommentRow,
  type LoanCommentTemplate,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

type Props = {
  applicationId?: string;
  loanId?: string;
  readOnly?: boolean;
  onChanged?: () => void;
};

function relativeTs(ts: string): string {
  const diff = Date.now() - new Date(ts).getTime();
  const min = Math.floor(diff / 60_000);
  if (min < 1) return 'just now';
  if (min < 60) return `${min}m ago`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

export default function CommentsTab({ applicationId, loanId, readOnly, onChanged }: Props) {
  const { hasPermission, user } = useAuth();
  const canPost = hasPermission('loans:apply');
  const myUserID = user?.id;
  const [items, setItems] = useState<LoanCommentRow[]>([]);
  const [templates, setTemplates] = useState<LoanCommentTemplate[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [search, setSearch] = useState('');

  async function refresh() {
    setLoading(true); setErr(null);
    try {
      const r = applicationId ? await listApplicationComments(applicationId)
              : loanId        ? await listLoanComments(loanId)
              : { items: [], total: 0 };
      setItems(r.items);
      if (templates.length === 0) {
        try { setTemplates((await listLoanCommentTemplates()).items); } catch {}
      }
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Failed to load comments.');
    } finally { setLoading(false); }
  }
  useEffect(() => { void refresh(); }, [applicationId, loanId]);

  async function doSearch(q: string) {
    if (!q.trim()) { void refresh(); return; }
    try {
      const r = await searchLoanComments(q, { applicationId, loanId });
      setItems(r.items);
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Search failed.');
    }
  }

  const pinned = items.filter((c) => c.pinned && !c.is_deleted);
  const unpinned = items.filter((c) => !c.pinned);

  return (
    <div>
      {err && <div className="alert alert-error" style={{ marginBottom: 8 }}>{err}</div>}

      {canPost && !readOnly && (
        <Composer
          templates={templates}
          onPost={async (body, visibility, templateId, parentId) => {
            try {
              if (applicationId) await postApplicationComment(applicationId, { body, visibility, template_id: templateId, parent_id: parentId });
              else if (loanId)   await postLoanComment(loanId, { body, visibility, template_id: templateId, parent_id: parentId });
              await refresh();
              onChanged?.();
            } catch (e: any) {
              alert(e?.response?.data?.error?.message || e?.message || 'Post failed.');
            }
          }}
        />
      )}

      <div style={{ display: 'flex', gap: 6, alignItems: 'center', marginTop: 12 }}>
        <input
          className="input"
          style={{ flex: 1 }}
          placeholder="Search comments…"
          value={search}
          onChange={(e) => { setSearch(e.target.value); }}
          onKeyDown={(e) => { if (e.key === 'Enter') void doSearch(search); }}
        />
        {search && <button className="btn btn-sm" onClick={() => { setSearch(''); void refresh(); }}>Clear</button>}
      </div>

      {loading ? (
        <div className="muted" style={{ marginTop: 12 }}>Loading…</div>
      ) : items.length === 0 ? (
        <div className="empty" style={{ padding: 14 }}>No comments yet.</div>
      ) : (
        <>
          {pinned.length > 0 && (
            <div style={{ marginTop: 12 }}>
              <div className="muted tiny" style={{ marginBottom: 4 }}>📌 Pinned</div>
              {pinned.map((c) => (
                <CommentRow key={c.id} c={c} myUserID={myUserID} readOnly={readOnly}
                  onChanged={() => { void refresh(); onChanged?.(); }} />
              ))}
            </div>
          )}
          <div style={{ marginTop: 12 }}>
            {unpinned.map((c) => (
              <CommentRow key={c.id} c={c} myUserID={myUserID} readOnly={readOnly}
                onChanged={() => { void refresh(); onChanged?.(); }} />
            ))}
          </div>
        </>
      )}
    </div>
  );
}

function Composer({ templates, onPost }: {
  templates: LoanCommentTemplate[];
  onPost: (body: string, visibility: 'internal' | 'external', templateId?: string, parentId?: string) => Promise<void>;
}) {
  const [visibility, setVisibility] = useState<'internal' | 'external'>('internal');
  const [body, setBody] = useState('');
  const [templateID, setTemplateID] = useState<string>('');
  const [busy, setBusy] = useState(false);

  const filteredTemplates = templates.filter((t) => t.visibility === visibility);
  const charCount = body.length;
  const overSmsLimit = visibility === 'external' && charCount > 160;

  // Tone tokens drive the visibility-pill banner + composer accent.
  const tone = visibility === 'external'
    ? { bg: 'var(--info-bg, #eaf3fb)',   fg: 'var(--info-fg, #0b5394)',   label: '📤 External — sends an SMS to the member' }
    : { bg: 'var(--surface-2, #f7f7f9)', fg: 'var(--muted, #6b7280)',     label: '🔒 Internal — visible to officers only' };

  return (
    <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
      {/* Header — visibility pill + segmented switcher + template */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 12,
        padding: '10px 14px',
        background: tone.bg, color: tone.fg,
        borderBottom: '1px solid var(--border, #eee)',
        flexWrap: 'wrap',
      }}>
        <strong style={{ fontSize: 13 }}>{tone.label}</strong>
        <div style={{ flex: 1 }} />
        <div role="tablist" style={{
          display: 'flex', gap: 0,
          background: 'var(--surface, white)',
          border: '1px solid var(--border, #ddd)',
          borderRadius: 8, overflow: 'hidden',
        }}>
          {(['internal', 'external'] as const).map((v) => (
            <button
              key={v}
              role="tab"
              aria-selected={visibility === v}
              onClick={() => setVisibility(v)}
              style={{
                padding: '6px 12px', border: 'none', cursor: 'pointer',
                background: visibility === v ? 'var(--accent, #2563eb)' : 'transparent',
                color: visibility === v ? 'white' : 'var(--muted)',
                fontSize: 12, fontWeight: 600,
              }}
            >{v === 'internal' ? 'Internal' : 'Send to member'}</button>
          ))}
        </div>
        {filteredTemplates.length > 0 && (
          <select
            className="input"
            value={templateID}
            style={{ maxWidth: 200 }}
            onChange={(e) => {
              setTemplateID(e.target.value);
              const t = filteredTemplates.find((x) => x.id === e.target.value);
              if (t) setBody(t.body);
            }}
          >
            <option value="">Use a template…</option>
            {filteredTemplates.map((t) => <option key={t.id} value={t.id}>{t.label}</option>)}
          </select>
        )}
      </div>

      {/* Body */}
      <div style={{ padding: 12 }}>
        <textarea
          className="input"
          rows={4}
          value={body}
          onChange={(e) => setBody(e.target.value)}
          placeholder={visibility === 'external'
            ? 'Message to send to the member by SMS. Placeholders like {member_name} are interpolated automatically.'
            : 'Internal note — visible only to officers with loan access.'
          }
          style={{ resize: 'vertical', minHeight: 80 }}
        />
      </div>

      {/* Footer — char counter + post */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 12,
        padding: '10px 14px',
        background: 'var(--surface-2, #fafafa)',
        borderTop: '1px solid var(--border, #eee)',
      }}>
        {visibility === 'external' && (
          <span
            className="tiny"
            style={{ color: overSmsLimit ? 'var(--danger-fg, #b42318)' : 'var(--muted, #6b7280)' }}
          >
            {charCount} / 160 SMS chars{overSmsLimit && ' — message will split into multiple SMS'}
          </span>
        )}
        <div style={{ flex: 1 }} />
        <button
          className="btn btn-primary btn-sm"
          disabled={busy || !body.trim()}
          onClick={async () => {
            setBusy(true);
            try { await onPost(body, visibility, templateID || undefined); setBody(''); setTemplateID(''); }
            finally { setBusy(false); }
          }}
        >{busy ? 'Posting…' : (visibility === 'external' ? 'Send & save' : 'Post note')}</button>
      </div>
    </div>
  );
}

function CommentRow({ c, myUserID, readOnly, onChanged }: { c: LoanCommentRow; myUserID?: string; readOnly?: boolean; onChanged: () => void }) {
  const own = myUserID != null && c.author_user_id === myUserID;
  const [editing, setEditing] = useState(false);
  const [editBody, setEditBody] = useState(c.body);
  const [busy, setBusy] = useState(false);

  const visibilityChip = c.visibility === 'internal'
    ? <span style={{ background: 'var(--surface-2, #f7f7f9)', color: 'var(--muted, #888)', padding: '2px 6px', borderRadius: 8, fontSize: 10 }}>INTERNAL</span>
    : c.author_member_id
      ? <span style={{ background: 'var(--info-bg, #eaf3fb)', color: 'var(--info-fg, #0b5394)', padding: '2px 6px', borderRadius: 8, fontSize: 10 }}>📥 MEMBER REPLY</span>
      : <span style={{ background: 'var(--success-bg, #e7f4ec)', color: 'var(--success-fg, #146c43)', padding: '2px 6px', borderRadius: 8, fontSize: 10 }}>📤 SENT TO MEMBER</span>;

  return (
    <div style={{
      padding: '10px 12px',
      borderBottom: '1px solid var(--border, #eee)',
      background: c.pinned ? 'var(--warning-bg, #fff8e6)' : undefined,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
        <strong>{c.author_name || (c.author_member_id ? 'Member' : 'Officer')}</strong>
        {visibilityChip}
        <span className="muted tiny">{relativeTs(c.posted_at)}</span>
        {c.edited_at && <span className="muted tiny">(edited)</span>}
        <div style={{ flex: 1 }} />
        {c.visibility === 'external' && !c.author_member_id && (
          <span className="muted tiny">{c.member_read_at ? `Member read ${relativeTs(c.member_read_at)}` : 'Not yet read'}</span>
        )}
      </div>
      {editing ? (
        <>
          <textarea className="input" rows={3} value={editBody} onChange={(e) => setEditBody(e.target.value)} />
          <div style={{ display: 'flex', gap: 6, marginTop: 6 }}>
            <button className="btn btn-sm btn-primary" disabled={busy} onClick={async () => {
              setBusy(true);
              try { await editLoanComment(c.id, editBody); setEditing(false); onChanged(); }
              catch (e: any) { alert(e?.response?.data?.error?.message || e?.message || 'Edit failed.'); }
              finally { setBusy(false); }
            }}>Save</button>
            <button className="btn btn-sm" disabled={busy} onClick={() => { setEditBody(c.body); setEditing(false); }}>Cancel</button>
          </div>
        </>
      ) : (
        <div style={{ whiteSpace: 'pre-wrap' }}>{c.is_deleted ? <em className="muted">[deleted]</em> : c.body}</div>
      )}
      {!editing && !readOnly && (
        <div style={{ display: 'flex', gap: 6, marginTop: 6 }}>
          {!c.is_deleted && own && <button className="btn btn-sm btn-link" disabled={busy} onClick={() => setEditing(true)}>Edit</button>}
          {!c.is_deleted && (
            <button className="btn btn-sm btn-link" disabled={busy} onClick={async () => {
              setBusy(true);
              try { await pinLoanComment(c.id, !c.pinned); onChanged(); }
              catch (e: any) { alert(e?.response?.data?.error?.message || e?.message || 'Pin failed.'); }
              finally { setBusy(false); }
            }}>{c.pinned ? 'Unpin' : 'Pin'}</button>
          )}
          {!c.is_deleted && own && (
            <button className="btn btn-sm btn-link" disabled={busy} onClick={async () => {
              if (!window.confirm('Delete this comment?')) return;
              setBusy(true);
              try { await deleteLoanComment(c.id); onChanged(); }
              catch (e: any) { alert(e?.response?.data?.error?.message || e?.message || 'Delete failed.'); }
              finally { setBusy(false); }
            }}>Delete</button>
          )}
        </div>
      )}
    </div>
  );
}
