DROP FUNCTION IF EXISTS mpesa_credentials_read(uuid, mpesa_credential_kind);

DROP TABLE IF EXISTS mpesa_reversal_events;
DROP TABLE IF EXISTS mpesa_outbound_requests;
DROP TABLE IF EXISTS mpesa_distribution_runs;
DROP TABLE IF EXISTS mpesa_inbound_events;
DROP TABLE IF EXISTS mpesa_distribution_policies;
DROP TABLE IF EXISTS mpesa_paybill_credentials;
DROP TABLE IF EXISTS mpesa_paybills;

DROP TYPE IF EXISTS mpesa_reversal_status;
DROP TYPE IF EXISTS mpesa_reversal_direction;
DROP TYPE IF EXISTS mpesa_outbound_status;
DROP TYPE IF EXISTS mpesa_outbound_kind;
DROP TYPE IF EXISTS mpesa_distribution_status;
DROP TYPE IF EXISTS mpesa_resolver_status;
DROP TYPE IF EXISTS mpesa_credential_kind;
DROP TYPE IF EXISTS mpesa_paybill_status;
DROP TYPE IF EXISTS mpesa_paybill_purpose;
DROP TYPE IF EXISTS mpesa_environment;
