// TechDetails — collapsed-by-default disclosure for raw UUIDs and
// IDs that engineering / auditors need but officers don't. Drop in
// any context where we previously rendered `value.slice(0, 8)+'…'`
// as the primary label.

import type { ReactNode } from 'react';

export function TechDetails({
  summary = 'Technical details',
  children,
}: {
  summary?: string;
  children: ReactNode;
}) {
  return (
    <details style={{ marginTop: 6 }}>
      <summary className="muted tiny" style={{ cursor: 'pointer', userSelect: 'none' }}>
        {summary}
      </summary>
      <div style={{ marginTop: 4 }}>{children}</div>
    </details>
  );
}

// Re-export from a barrel so consumers can `import { TechDetails }
// from '../components/refs'` without listing every file.
export default TechDetails;
