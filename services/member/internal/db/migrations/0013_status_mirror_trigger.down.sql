DROP TRIGGER IF EXISTS trg_org_members_mirror_status_to_counterparty ON org_members;
DROP TRIGGER IF EXISTS trg_members_mirror_status_to_counterparty ON members;
DROP FUNCTION IF EXISTS mirror_status_to_counterparty();
