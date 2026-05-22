// Role-label registry — turns the snake_case role codes the backend
// ships (tenant_owner, branch_manager, loan_reviewer_2030, …) into
// human-readable text for the UI. Co-located with the enum's
// canonical list so adding a new seed role is a one-file edit.
//
// Source of truth for the seed codes is
// services/identity/internal/db/migrations/0002_seed_rbac.up.sql.
// Tenant-defined custom roles are not in the map and fall through to
// the algorithmic humaniser below.

const ROLE_LABELS: Readonly<Record<string, string>> = {
  // Platform
  platform_admin:      'Platform admin',
  // Tenant — seeded by the rbac migration
  tenant_owner:        'Tenant owner',
  sacco_admin:         'SACCO admin',
  branch_manager:      'Branch manager',
  credit_officer:      'Credit officer',
  teller:              'Teller',
  accountant:          'Accountant',
  auditor:             'Auditor',
  collections_officer: 'Collections officer',
  loan_reviewer:       'Loan reviewer',
};

// roleLabel turns a role code into something a non-engineer would read.
//   - Known code → its registered label.
//   - Trailing digits (custom roles like "loan_reviewer_2030") get
//     pulled out into a parenthetical year/batch suffix, with the
//     stem sentence-cased.
//   - Anything else → underscores replaced with spaces and returned
//     verbatim (lowercase preserved, so an acronym typed as "api_admin"
//     doesn't get accidentally title-cased into "Api admin").
export function roleLabel(code: string): string {
  if (!code) return '';
  const registered = ROLE_LABELS[code];
  if (registered) return registered;

  const trailingDigits = code.match(/^(.+?)_(\d+)$/);
  if (trailingDigits) {
    const stem = trailingDigits[1];
    const suffix = trailingDigits[2];
    // If the stem is itself a registered role ("loan_reviewer_2030"
    // → "loan_reviewer"), reuse that label so e.g. SACCO acronyms
    // don't get re-cased.
    const stemLabel = ROLE_LABELS[stem] ?? sentenceCase(stem.replace(/_+/g, ' '));
    return `${stemLabel} (${suffix})`;
  }

  return code.replace(/_+/g, ' ');
}

function sentenceCase(s: string): string {
  if (!s) return s;
  return s.charAt(0).toUpperCase() + s.slice(1);
}
