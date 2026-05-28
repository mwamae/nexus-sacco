DELETE FROM pdf_templates
 WHERE document_type IN (
   'loan_pre_collection_letter',
   'loan_demand_letter',
   'loan_final_demand_letter',
   'loan_legal_notice_letter'
 );
