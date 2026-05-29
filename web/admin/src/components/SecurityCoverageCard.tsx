// Phase 1.5a — policy-aware security-coverage header card.
//
// Renders the SecurityCoverage payload returned by
// GET /v1/loan-applications/{app_id}/security-coverage.
//
// Per security_model:
//   none            → collapsed card "no external security required".
//   guarantor_only  → just the guarantor row, collateral muted.
//   collateral_only → just the collateral row, guarantor muted.
//   either          → both rows, status shows which side passes.
//   both            → both rows; both must show ✓ for overall pass.

import type { SecurityCoverage, SecurityModel } from '../api/client';

type Props = {
  data: SecurityCoverage;
};

const MODEL_LABEL: Record<SecurityModel, string> = {
  none: 'NONE',
  guarantor_only: 'GUARANTORS ONLY',
  collateral_only: 'COLLATERAL ONLY',
  either: 'EITHER',
  both: 'BOTH',
};

function formatPolicyLine(model: SecurityModel, gMin: string, cMin: string): string {
  switch (model) {
    case 'none':
      return 'No external security required for this product.';
    case 'guarantor_only':
      return `Guarantor cover must be ≥ ${gMin}%`;
    case 'collateral_only':
      return `Collateral FSV cover must be ≥ ${cMin}%`;
    case 'either':
      return `Either guarantor cover ≥ ${gMin}% OR collateral FSV cover ≥ ${cMin}%`;
    case 'both':
      return `Both guarantor cover ≥ ${gMin}% AND collateral FSV cover ≥ ${cMin}%`;
  }
}

function CoverageRow({
  label, pledged, pct, min, passes, required, muted,
}: {
  label: string;
  pledged: string;
  pct: string;
  min: string;
  passes: boolean;
  required: boolean; // does this row count against the policy?
  muted?: boolean;
}) {
  const pillBg = !required
    ? 'var(--surface-2, #f7f7f9)'
    : passes
      ? 'var(--success-bg, #e7f4ec)'
      : 'var(--danger-bg, #fdecea)';
  const pillFg = !required
    ? 'var(--muted, #888)'
    : passes
      ? 'var(--success-fg, #146c43)'
      : 'var(--danger-fg, #b42318)';
  const pillText = !required ? 'n/a' : passes ? '✓ passes' : '✗ fails';
  return (
    <div style={{
      display: 'grid', gridTemplateColumns: '1fr auto auto auto',
      gap: 12, padding: '8px 0',
      borderBottom: '1px solid var(--border, #eee)',
      opacity: muted ? 0.5 : 1,
    }}>
      <div>
        <div style={{ fontWeight: 600 }}>{label}</div>
        <div className="muted tiny">KES {fmt(pledged)} pledged</div>
      </div>
      <div className="num" style={{ minWidth: 70 }}>
        <div style={{ fontWeight: 600 }}>{pct}%</div>
        <div className="muted tiny">coverage</div>
      </div>
      <div className="num muted tiny" style={{ minWidth: 90 }}>
        target ≥ {min}%
      </div>
      <span style={{
        background: pillBg, color: pillFg,
        padding: '3px 8px', borderRadius: 12,
        fontSize: 11, fontWeight: 600,
        alignSelf: 'center',
      }}>{pillText}</span>
    </div>
  );
}

function fmt(s: string): string {
  if (!s) return '0';
  const n = parseFloat(s);
  if (Number.isNaN(n)) return s;
  return n.toLocaleString('en-KE', { maximumFractionDigits: 2 });
}

export default function SecurityCoverageCard({ data }: Props) {
  const { coverage, policy, result } = data;
  const model = policy.security_model;
  const guarantorRequired = model === 'guarantor_only' || model === 'either' || model === 'both';
  const collateralRequired = model === 'collateral_only' || model === 'either' || model === 'both';

  // Status pill at the bottom.
  let statusBg = 'var(--success-bg, #e7f4ec)';
  let statusFg = 'var(--success-fg, #146c43)';
  let statusLabel = '✓ MEETS POLICY';
  if (!result.policy_met) {
    statusBg = 'var(--danger-bg, #fdecea)';
    statusFg = 'var(--danger-fg, #b42318)';
    statusLabel = '✗ POLICY NOT MET';
  } else if (model === 'either' && !(result.guarantor_passes && result.collateral_passes)) {
    // either-mode passing on one side: warn-amber strip to show it's
    // a single-rail pass.
    statusBg = 'var(--warning-bg, #fff4d6)';
    statusFg = 'var(--warning-fg, #8a6d00)';
    statusLabel = '✓ MEETS POLICY (one side)';
  }

  if (model === 'none') {
    return (
      <div className="card" style={{ padding: 16, background: 'var(--surface-2, #f7f7f9)' }}>
        <div className="muted tiny">Security policy</div>
        <div style={{ fontWeight: 600, marginTop: 4 }}>NONE</div>
        <div className="muted" style={{ marginTop: 6 }}>
          No external security required for this product.
        </div>
      </div>
    );
  }

  return (
    <div className="card" style={{ padding: 16, marginBottom: 12 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
        <div>
          <div className="muted tiny">Security policy</div>
          <div style={{ fontWeight: 600 }}>{MODEL_LABEL[model]}</div>
        </div>
        <div className="muted tiny" style={{ textAlign: 'right' }}>
          Loan amount<br />
          <span style={{ fontWeight: 600, color: 'var(--fg)' }}>
            KES {fmt(coverage.loan_amount)}
          </span>
          <span style={{ marginLeft: 6 }}>({coverage.loan_amount_basis})</span>
        </div>
      </div>
      <div className="muted tiny" style={{ marginBottom: 12 }}>
        {formatPolicyLine(model, policy.min_guarantor_cover_pct, policy.min_collateral_cover_pct)}
      </div>

      <CoverageRow
        label="Guarantor pledges"
        pledged={coverage.guarantor_pledged}
        pct={result.guarantor_pct}
        min={policy.min_guarantor_cover_pct}
        passes={result.guarantor_passes}
        required={guarantorRequired}
        muted={!guarantorRequired}
      />
      <CoverageRow
        label="Collateral FSV"
        pledged={coverage.collateral_fsv}
        pct={result.collateral_pct}
        min={policy.min_collateral_cover_pct}
        passes={result.collateral_passes}
        required={collateralRequired}
        muted={!collateralRequired}
      />

      <div style={{
        marginTop: 12, padding: '10px 12px',
        borderRadius: 6, background: statusBg, color: statusFg,
        display: 'flex', justifyContent: 'space-between', gap: 12,
      }}>
        <span style={{ fontWeight: 700 }}>{statusLabel}</span>
        <span style={{ flex: 1, fontSize: 13 }}>{result.reason}</span>
      </div>
    </div>
  );
}
