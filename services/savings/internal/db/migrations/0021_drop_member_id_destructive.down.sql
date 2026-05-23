-- DOWN: re-add member_id columns + indexes + triggers + function so a
-- rollback can keep the trigger-driven dual-write going. Backfills
-- member_id from the inverse bridge (members.counterparty_id ↔ id).
--
-- Heavy operation — a full rebuild of 21 columns + indexes — but
-- correctness wins over speed since this only runs on a rollback.

CREATE OR REPLACE FUNCTION populate_counterparty_id_from_member()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.counterparty_id IS NULL AND NEW.member_id IS NOT NULL THEN
    SELECT counterparty_id INTO NEW.counterparty_id
      FROM members WHERE id = NEW.member_id;
  END IF;
  RETURN NEW;
END; $$;

-- Per-table restoration.
ALTER TABLE share_accounts        ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE share_accounts sa SET member_id = m.id FROM members m WHERE m.counterparty_id = sa.counterparty_id;
ALTER TABLE share_accounts        ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX share_accounts_member_idx ON share_accounts(member_id);
CREATE UNIQUE INDEX share_accounts_tenant_id_member_id_key ON share_accounts(tenant_id, member_id);
CREATE TRIGGER trg_share_accounts_populate_counterparty BEFORE INSERT ON share_accounts FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE share_transactions    ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE share_transactions st SET member_id = m.id FROM members m WHERE m.counterparty_id = st.counterparty_id;
ALTER TABLE share_transactions    ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX share_txn_member_idx ON share_transactions(member_id, posted_at DESC);
CREATE TRIGGER trg_share_transactions_populate_counterparty BEFORE INSERT ON share_transactions FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE share_certificates    ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE share_certificates sc SET member_id = m.id FROM members m WHERE m.counterparty_id = sc.counterparty_id;
ALTER TABLE share_certificates    ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE TRIGGER trg_share_certificates_populate_counterparty BEFORE INSERT ON share_certificates FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE deposit_accounts      ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE deposit_accounts da SET member_id = m.id FROM members m WHERE m.counterparty_id = da.counterparty_id;
ALTER TABLE deposit_accounts      ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX deposit_accounts_member_idx ON deposit_accounts(member_id);
CREATE TRIGGER trg_deposit_accounts_populate_counterparty BEFORE INSERT ON deposit_accounts FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE deposit_transactions  ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE deposit_transactions dt SET member_id = m.id FROM members m WHERE m.counterparty_id = dt.counterparty_id;
ALTER TABLE deposit_transactions  ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX deposit_txn_member_posted_idx ON deposit_transactions(member_id, posted_at DESC);
CREATE TRIGGER trg_deposit_transactions_populate_counterparty BEFORE INSERT ON deposit_transactions FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE deposit_daily_balances ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE deposit_daily_balances ddb SET member_id = m.id FROM members m WHERE m.counterparty_id = ddb.counterparty_id;
ALTER TABLE deposit_daily_balances ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE TRIGGER trg_deposit_daily_balances_populate_counterparty BEFORE INSERT ON deposit_daily_balances FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE loans                 ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE loans l SET member_id = m.id FROM members m WHERE m.counterparty_id = l.counterparty_id;
ALTER TABLE loans                 ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX loans_member_idx ON loans(member_id, status);
CREATE TRIGGER trg_loans_populate_counterparty BEFORE INSERT ON loans FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE loan_applications     ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE loan_applications la SET member_id = m.id FROM members m WHERE m.counterparty_id = la.counterparty_id;
ALTER TABLE loan_applications     ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX loan_apps_member_idx ON loan_applications(member_id, created_at DESC);
CREATE TRIGGER trg_loan_applications_populate_counterparty BEFORE INSERT ON loan_applications FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE loan_transactions     ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE loan_transactions lt SET member_id = m.id FROM members m WHERE m.counterparty_id = lt.counterparty_id;
ALTER TABLE loan_transactions     ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX loan_txn_member_idx ON loan_transactions(member_id, posted_at DESC);
CREATE TRIGGER trg_loan_transactions_populate_counterparty BEFORE INSERT ON loan_transactions FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE loan_guarantees       ADD COLUMN guarantor_member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE loan_guarantees lg SET guarantor_member_id = m.id FROM members m WHERE m.counterparty_id = lg.guarantor_counterparty_id;
ALTER TABLE loan_guarantees       ALTER COLUMN guarantor_counterparty_id DROP NOT NULL;
CREATE INDEX loan_guarantees_member_idx ON loan_guarantees(guarantor_member_id, status);

ALTER TABLE loan_collection_cases ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE loan_collection_cases lcc SET member_id = m.id FROM members m WHERE m.counterparty_id = lcc.counterparty_id;
ALTER TABLE loan_collection_cases ALTER COLUMN counterparty_id DROP NOT NULL;

ALTER TABLE loan_writeoffs        ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE loan_writeoffs lw SET member_id = m.id FROM members m WHERE m.counterparty_id = lw.counterparty_id;
ALTER TABLE loan_writeoffs        ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE TRIGGER trg_loan_writeoffs_populate_counterparty BEFORE INSERT ON loan_writeoffs FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE dividend_run_lines    ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE dividend_run_lines drl SET member_id = m.id FROM members m WHERE m.counterparty_id = drl.counterparty_id;
ALTER TABLE dividend_run_lines    ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX dividend_run_lines_member_idx ON dividend_run_lines(member_id, run_id);

ALTER TABLE interest_run_lines    ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE interest_run_lines irl SET member_id = m.id FROM members m WHERE m.counterparty_id = irl.counterparty_id;
ALTER TABLE interest_run_lines    ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX interest_run_lines_member_idx ON interest_run_lines(member_id, run_id);

ALTER TABLE tax_payable_ledger    ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE tax_payable_ledger tpl SET member_id = m.id FROM members m WHERE m.counterparty_id = tpl.counterparty_id;
ALTER TABLE tax_payable_ledger    ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX tax_payable_member_idx ON tax_payable_ledger(member_id, posted_at DESC);
CREATE TRIGGER trg_tax_payable_ledger_populate_counterparty BEFORE INSERT ON tax_payable_ledger FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE provision_run_lines   ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE provision_run_lines prl SET member_id = m.id FROM members m WHERE m.counterparty_id = prl.counterparty_id;
ALTER TABLE provision_run_lines   ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE TRIGGER trg_provision_run_lines_populate_counterparty BEFORE INSERT ON provision_run_lines FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();
