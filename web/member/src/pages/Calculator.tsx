// Loan calculator — pure client-side math; useful pre-application
// so a member can see the estimated installment. Real engine uses
// reducing-balance + grace periods; this preview is directional.

import { useMemo, useState } from 'react';
import { estimateInstallment } from '../api';

export default function Calculator() {
  const [amount, setAmount] = useState('100000');
  const [term, setTerm] = useState('12');
  const [rate, setRate] = useState('14');

  const result = useMemo(() => estimateInstallment(
    parseFloat(amount) || 0,
    parseFloat(rate) || 0,
    parseInt(term, 10) || 0,
  ), [amount, term, rate]);

  return (
    <div className="card">
      <h2 style={{ marginTop: 0 }}>Loan calculator</h2>
      <p style={{ color: '#555', fontSize: 13 }}>
        Estimate your monthly installment before applying. This is a directional
        preview — your SACCO's final figures will differ based on fees, grace
        periods, and reducing-balance vs flat-rate methods.
      </p>
      <label>
        <div style={{ fontSize: 12, color: '#555', marginBottom: 4 }}>Loan amount (KES)</div>
        <input className="input" type="number" value={amount} onChange={(e) => setAmount(e.target.value)} />
      </label>
      <label>
        <div style={{ fontSize: 12, color: '#555', marginBottom: 4 }}>Term (months)</div>
        <input className="input" type="number" value={term} onChange={(e) => setTerm(e.target.value)} />
      </label>
      <label>
        <div style={{ fontSize: 12, color: '#555', marginBottom: 4 }}>Interest rate (% per annum)</div>
        <input className="input" type="number" step="0.1" value={rate} onChange={(e) => setRate(e.target.value)} />
      </label>
      <div style={{ marginTop: 16, padding: 12, background: '#f0f4fa', borderRadius: 4 }}>
        <div className="row"><span className="lbl">Monthly installment</span><span className="mono">KES {result.monthlyInstallment.toLocaleString()}</span></div>
        <div className="row"><span className="lbl">Total interest over term</span><span className="mono">KES {result.totalInterest.toLocaleString()}</span></div>
        <div className="row"><span className="lbl">Total payable</span><span className="mono">KES {result.totalPayable.toLocaleString()}</span></div>
      </div>
    </div>
  );
}
