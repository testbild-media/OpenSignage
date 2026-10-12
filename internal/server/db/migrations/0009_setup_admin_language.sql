-- The setup used to copy the server language into the first administrator's own account
-- language, so a later change of the server language (Settings → General) did not reach
-- that account. An account the setup created and nobody changed since follows the server
-- default again (language NULL); accounts whose language was chosen on purpose keep it.
UPDATE users SET language = NULL
WHERE language IS NOT NULL
  AND created_at = updated_at
  AND id IN (SELECT object_id FROM audit_log WHERE action = 'setup.completed');
