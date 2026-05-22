-- Backfill counterparties from the existing members + org_members
-- tables. Idempotent — the WHERE counterparty_id IS NULL guard means
-- re-running this migration does nothing for rows already mapped.
--
-- Run order:
--   1. Mint CP numbers per tenant via share_number_seq(kind='counterparty').
--   2. INSERT a counterparties row for each unmapped members row.
--   3. UPDATE members.counterparty_id pointing at the new row.
--   4. Repeat for org_members.
--
-- The mapping is deterministic + audit-able from legacy_id: every
-- counterparty preserves its M-* or ORG-* number in legacy_id, and
-- every legacy row points at exactly one counterparty.

DO $$
DECLARE
  r       RECORD;
  new_cp  uuid;
  new_no  text;
  seq_val int;
  yr      int;
  cp_kind counterparty_kind;
  cp_stat counterparty_status;
BEGIN
  -- ─────────── members → counterparties (kind=individual) ───────────
  FOR r IN
    SELECT m.id            AS member_id,
           m.tenant_id,
           m.member_no,
           m.full_name,
           m.status::text  AS status_text,
           m.id_doc_kind::text AS id_doc_kind,
           m.id_doc_number,
           m.kra_pin,
           m.gender::text  AS gender,
           m.date_of_birth,
           m.phone,
           m.email,
           m.county,
           m.sub_county,
           m.physical_address,
           m.employment_status,
           m.employer,
           m.payroll_no,
           m.job_title,
           m.approved_at,
           m.created_at,
           m.created_by
      FROM members m
     WHERE m.counterparty_id IS NULL
     ORDER BY m.tenant_id, m.created_at
  LOOP
    yr := EXTRACT(YEAR FROM r.created_at)::int;
    -- Use the year the member was created so cp_number ranges align
    -- with historical activity rather than today's clock.
    INSERT INTO share_number_seq (tenant_id, kind, year, last_value)
    VALUES (r.tenant_id, 'counterparty', yr, 1)
    ON CONFLICT (tenant_id, kind, year)
    DO UPDATE SET last_value = share_number_seq.last_value + 1
    RETURNING last_value INTO seq_val;
    new_no := format('CP-%s-%s', yr, lpad(seq_val::text, 5, '0'));

    -- member_status enum values map 1:1 to counterparty_status.
    cp_stat := r.status_text::counterparty_status;

    INSERT INTO counterparties (
      tenant_id, cp_number, legacy_id, kind, display_name,
      status, kyc_state, risk_band,
      individual, contact,
      joined_at, created_at, created_by
    ) VALUES (
      r.tenant_id, new_no, r.member_no, 'individual'::counterparty_kind, r.full_name,
      cp_stat,
      CASE WHEN r.approved_at IS NOT NULL THEN 'verified'::counterparty_kyc_state
           ELSE 'not_started'::counterparty_kyc_state END,
      'n_a'::counterparty_risk_band,
      jsonb_strip_nulls(jsonb_build_object(
        'gender',           NULLIF(r.gender,''),
        'date_of_birth',    r.date_of_birth,
        'id_doc_kind',      NULLIF(r.id_doc_kind,''),
        'id_doc_number',    NULLIF(r.id_doc_number,''),
        'kra_pin',          NULLIF(r.kra_pin,''),
        'employment_status', NULLIF(r.employment_status,''),
        'employer',         NULLIF(r.employer,''),
        'payroll_no',       NULLIF(r.payroll_no,''),
        'job_title',        NULLIF(r.job_title,'')
      )),
      jsonb_strip_nulls(jsonb_build_object(
        'phone',            NULLIF(r.phone,''),
        'email',            NULLIF(r.email::text,''),
        'county',           NULLIF(r.county,''),
        'sub_county',       NULLIF(r.sub_county,''),
        'physical_address', NULLIF(r.physical_address,'')
      )),
      COALESCE(r.approved_at, r.created_at),
      r.created_at, r.created_by
    )
    RETURNING id INTO new_cp;

    UPDATE members SET counterparty_id = new_cp WHERE id = r.member_id;

    RAISE NOTICE 'backfill member: % (% / %) → counterparty %', r.full_name, r.member_no, new_no, new_cp;
  END LOOP;

  -- ─────────── org_members → counterparties (kind=<mapped>) ───────────
  FOR r IN
    SELECT o.id                    AS org_id,
           o.tenant_id,
           o.org_no,
           o.registered_name,
           o.trading_name,
           o.kind::text             AS org_kind_text,
           o.registration_no,
           o.date_of_registration,
           o.date_of_operation,
           o.industry,
           o.nature_of_business,
           o.member_count           AS org_member_count,
           o.employee_count,
           o.status::text           AS status_text,
           o.kyc_status::text       AS kyc_text,
           o.risk_category::text    AS risk_text,
           o.blacklisted,
           o.blacklist_reason,
           o.dormant_since,
           o.physical_address,
           o.postal_address,
           o.county,
           o.sub_county,
           o.ward,
           o.approved_at,
           o.created_at,
           o.created_by
      FROM org_members o
     WHERE o.counterparty_id IS NULL
     ORDER BY o.tenant_id, o.created_at
  LOOP
    -- Collapse the 9-value legacy org_kind into the 6-value
    -- counterparty_kind. Anything we can't map cleanly falls to 'other'.
    cp_kind := CASE r.org_kind_text
                  WHEN 'group'        THEN 'chama'::counterparty_kind
                  WHEN 'chama'        THEN 'chama'::counterparty_kind
                  WHEN 'ltd'          THEN 'company'::counterparty_kind
                  WHEN 'sole_prop'    THEN 'company'::counterparty_kind
                  WHEN 'cooperative'  THEN 'company'::counterparty_kind
                  WHEN 'ngo'          THEN 'ngo'::counterparty_kind
                  WHEN 'church'       THEN 'church'::counterparty_kind
                  WHEN 'school'       THEN 'school'::counterparty_kind
                  WHEN 'sacco'        THEN 'other'::counterparty_kind
                  ELSE                     'other'::counterparty_kind
               END;

    -- org_status values: pending | active | suspended | closed | rejected | dormant.
    -- counterparty_status doesn't have 'closed' — map to 'exited'.
    cp_stat := CASE r.status_text
                  WHEN 'closed'  THEN 'exited'::counterparty_status
                  ELSE r.status_text::counterparty_status
               END;

    yr := EXTRACT(YEAR FROM r.created_at)::int;
    INSERT INTO share_number_seq (tenant_id, kind, year, last_value)
    VALUES (r.tenant_id, 'counterparty', yr, 1)
    ON CONFLICT (tenant_id, kind, year)
    DO UPDATE SET last_value = share_number_seq.last_value + 1
    RETURNING last_value INTO seq_val;
    new_no := format('CP-%s-%s', yr, lpad(seq_val::text, 5, '0'));

    INSERT INTO counterparties (
      tenant_id, cp_number, legacy_id, kind, display_name, trading_as,
      status, kyc_state, risk_band, registration_no,
      institution, contact,
      joined_at, created_at, created_by
    ) VALUES (
      r.tenant_id, new_no, r.org_no, cp_kind, r.registered_name, NULLIF(r.trading_name,''),
      cp_stat,
      CASE r.kyc_text
        WHEN 'not_started' THEN 'not_started'::counterparty_kyc_state
        WHEN 'in_review'   THEN 'in_review'::counterparty_kyc_state
        WHEN 'verified'    THEN 'verified'::counterparty_kyc_state
        WHEN 'rejected'    THEN 'rejected'::counterparty_kyc_state
      END,
      CASE r.risk_text
        WHEN 'low'    THEN 'low'::counterparty_risk_band
        WHEN 'medium' THEN 'medium'::counterparty_risk_band
        WHEN 'high'   THEN 'high'::counterparty_risk_band
      END,
      NULLIF(r.registration_no,''),
      jsonb_strip_nulls(jsonb_build_object(
        'legacy_org_kind',      r.org_kind_text,
        'registration_no',      NULLIF(r.registration_no,''),
        'date_of_registration', r.date_of_registration,
        'date_of_operation',    r.date_of_operation,
        'industry',             NULLIF(r.industry,''),
        'nature_of_business',   NULLIF(r.nature_of_business,''),
        'member_count',         r.org_member_count,
        'employee_count',       r.employee_count,
        'blacklisted',          r.blacklisted,
        'blacklist_reason',     NULLIF(r.blacklist_reason,''),
        'dormant_since',        r.dormant_since,
        -- Officials / signatories / mandate / banking / contacts live in
        -- their own org_* tables. The Go-side mirror writer materialises
        -- those into institution.officials[] / .signatories[] / etc. on
        -- subsequent edits. The backfill stamps a 'needs_sync' breadcrumb
        -- so the Go mirror knows to lazily refresh on first read.
        'needs_sync',           true
      )),
      jsonb_strip_nulls(jsonb_build_object(
        'county',           NULLIF(r.county,''),
        'sub_county',       NULLIF(r.sub_county,''),
        'ward',             NULLIF(r.ward,''),
        'physical_address', NULLIF(r.physical_address,''),
        'postal_address',   NULLIF(r.postal_address,'')
      )),
      COALESCE(r.approved_at, r.created_at),
      r.created_at, r.created_by
    )
    RETURNING id INTO new_cp;

    UPDATE org_members SET counterparty_id = new_cp WHERE id = r.org_id;

    RAISE NOTICE 'backfill org: % (% / %) → counterparty %', r.registered_name, r.org_no, new_no, new_cp;
  END LOOP;
END $$;
