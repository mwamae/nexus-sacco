DELETE FROM wf_levels      WHERE definition_id IN
  (SELECT id FROM wf_definitions WHERE process_kind = 'mpesa_reconciliation_diff');
DELETE FROM wf_definitions WHERE process_kind = 'mpesa_reconciliation_diff';

DROP TABLE IF EXISTS mpesa_reconciliation_diffs;
DROP TABLE IF EXISTS mpesa_statement_pulls;

DROP TYPE IF EXISTS mpesa_diff_status;
DROP TYPE IF EXISTS mpesa_diff_kind;
DROP TYPE IF EXISTS mpesa_statement_pull_status;
