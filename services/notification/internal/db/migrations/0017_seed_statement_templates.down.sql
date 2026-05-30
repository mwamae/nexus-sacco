DELETE FROM pdf_templates
 WHERE document_type IN (
   'deposit_statement',
   'share_statement',
   'interest_statement',
   'dividend_statement'
 );
