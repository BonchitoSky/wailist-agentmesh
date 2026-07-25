-- Narrows the kind CHECK back to its pre-000009 set. Only safe to run if no
-- debit_ledger rows have kind = 'x402_relay_cost' -- if any exist, this
-- ALTER fails with a constraint violation rather than silently truncating
-- data, which is the correct failure mode for a down-migration on a
-- production audit ledger.
ALTER TABLE debit_ledger DROP CONSTRAINT debit_ledger_kind_valid;
ALTER TABLE debit_ledger ADD CONSTRAINT debit_ledger_kind_valid CHECK (kind IN ('byok_flat_fee', 'x402_platform_fee'));
