import { describe, it, expect } from 'vitest';
import { roleLabel } from './roleLabels';

describe('roleLabel', () => {
  it('maps registered snake_case role codes to their human labels', () => {
    expect(roleLabel('accountant')).toBe('Accountant');
    expect(roleLabel('branch_manager')).toBe('Branch manager');
    expect(roleLabel('tenant_owner')).toBe('Tenant owner');
    expect(roleLabel('collections_officer')).toBe('Collections officer');
    expect(roleLabel('sacco_admin')).toBe('SACCO admin');
  });

  it('extracts trailing digits as a parenthetical suffix for custom roles', () => {
    expect(roleLabel('loan_reviewer_2030')).toBe('Loan reviewer (2030)');
    // Stem that isn't registered → algorithmic sentence-case.
    expect(roleLabel('temp_staff_001')).toBe('Temp staff (001)');
  });

  it('falls back to raw key with underscores → spaces for unknown codes', () => {
    expect(roleLabel('weird_role')).toBe('weird role');
    expect(roleLabel('api_admin')).toBe('api admin'); // no false title-casing
    expect(roleLabel('single')).toBe('single');
  });

  it('handles empty input safely', () => {
    expect(roleLabel('')).toBe('');
  });
});
