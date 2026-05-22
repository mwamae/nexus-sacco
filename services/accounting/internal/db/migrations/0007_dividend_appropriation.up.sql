-- ═══════════════════════════════════════════════════════════════════
-- Dividends as appropriation of surplus, not as P&L expense.
--
-- The seed CoA in 0001_init defined 5010 'Dividend Expense' under
-- class=expense. That is incorrect for a Kenyan SACCO operating under
-- the Cooperative Societies Act + SASRA prudential rules:
--
--   • Member share capital is equity (already correctly seeded at 3000)
--   • Dividends declared on those shares are an appropriation of
--     accumulated surplus, recorded by debiting Retained Earnings and
--     crediting Dividend Payable — not by debiting any expense account
--   • Dividends therefore do NOT appear on the Income Statement; they
--     show up in the Statement of Changes in Equity as a distribution
--
-- Action: deactivate 5010 so no posting rule (current or future) can
-- pick it accidentally. The row is preserved (not deleted) because
-- it may already be referenced by historical journal lines; the
-- is_active flag is the canonical "available for new posting"
-- signal in this codebase.
-- ═══════════════════════════════════════════════════════════════════

UPDATE chart_of_accounts
   SET is_active = false,
       description = 'DEPRECATED — dividends are an appropriation of surplus (DR 3010 Retained Earnings / CR 2220 Dividend Payable). See migration 0007.'
 WHERE code = '5010';
