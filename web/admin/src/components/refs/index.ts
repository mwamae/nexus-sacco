// Barrel for the human-label resolver components added to replace
// raw uuid slicing in user-facing UI. Each component is fully
// memoised + degrades gracefully on resolve failure.
//
// Backend-id symbol stays in TS land (the Counterparty type,
// counterparty_id field, etc.) — these components are the visual
// layer that turns ids into names.

export { MemberRef } from './MemberRef';
export { UserName } from './UserName';
export { TillLabel } from './TillLabel';
export { AccountRef } from './AccountRef';
export { LoanRef } from './LoanRef';
export { TechDetails } from './TechDetails';
