-- Roll back the 9 process_kinds seeded in 0010. Only deactivates
-- definitions that have never had any instance run against them —
-- once an instance exists, removing the definition would orphan
-- audit history. The escape hatch for a true rollback is to
-- deactivate the definition (active=false) instead.

DELETE FROM wf_levels
 WHERE definition_id IN (
   SELECT d.id FROM wf_definitions d
    WHERE d.process_kind IN (
      'share_lien', 'loan_disbursement', 'loan_repayment',
      'loan_settle', 'loan_reverse', 'fee_posting',
      'welfare_posting', 'application_fee', 'member_bosa_exit'
    )
      AND NOT EXISTS (SELECT 1 FROM wf_instances i WHERE i.definition_id = d.id)
 );

DELETE FROM wf_definitions
 WHERE process_kind IN (
   'share_lien', 'loan_disbursement', 'loan_repayment',
   'loan_settle', 'loan_reverse', 'fee_posting',
   'welfare_posting', 'application_fee', 'member_bosa_exit'
 )
   AND NOT EXISTS (SELECT 1 FROM wf_instances i WHERE i.definition_id = wf_definitions.id);
