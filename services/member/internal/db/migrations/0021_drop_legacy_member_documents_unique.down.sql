CREATE UNIQUE INDEX IF NOT EXISTS member_documents_counterparty_id_kind_key
  ON member_documents (counterparty_id, kind);
