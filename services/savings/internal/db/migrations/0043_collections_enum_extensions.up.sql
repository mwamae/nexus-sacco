-- Phase 4 follow-up — restore the data-shape distinctions that the
-- "extend 0007" reconciliation collapsed. Strictly additive: ALTER
-- TYPE ADD VALUE doesn't touch existing rows or break existing queries.
--
-- Two enums extended:
--
--   1. loan_contact_outcome — visit outcomes the legacy enum couldn't
--      express. The handler had been collapsing:
--        not_found_work   → visited_not_home  (loses workplace distinction)
--        moved            → wrong_number      (semantically wrong)
--      Now both have proper values. The handler's normaliseVisitOutcome
--      stops collapsing them after this migration applies.
--
--   2. loan_collection_event_kind — event kinds for surfaces the events
--      table couldn't represent. Previously:
--        manual SMS → events.kind='auto_sms' with payload.trigger='manual'
--                     (queries grouping by kind can't distinguish system
--                     from officer sends)
--        calls + visits → only loan_collection_contacts (kind=call /
--                     in_person_visit); never appear in
--                     loan_collection_events at all (queries against
--                     events miss them entirely)
--      Now: manual_sms / manual_email are first-class kinds; calls
--      and visits also emit a parallel call_attempt / field_visit
--      event row linked back to the contact via
--      details.source_contact_id (the timeline UNION dedupes via that
--      pointer so nothing renders twice).
--
-- The new enum values are NOT used by any prior row — only new writes
-- emit them. Existing rows keep their original enum values.

ALTER TYPE loan_contact_outcome ADD VALUE IF NOT EXISTS 'not_found_work';
ALTER TYPE loan_contact_outcome ADD VALUE IF NOT EXISTS 'moved';

ALTER TYPE loan_collection_event_kind ADD VALUE IF NOT EXISTS 'manual_sms';
ALTER TYPE loan_collection_event_kind ADD VALUE IF NOT EXISTS 'manual_email';
ALTER TYPE loan_collection_event_kind ADD VALUE IF NOT EXISTS 'call_attempt';
ALTER TYPE loan_collection_event_kind ADD VALUE IF NOT EXISTS 'field_visit';
