-- Bumpable epoch so multi-replica engines clear in-memory transaction state
-- after an identity-dimension reset wipe. Not an operator-facing setting.
-- NOTE: keep semicolons out of comments.

ALTER TABLE system_settings
    ADD COLUMN IF NOT EXISTS transaction_inventory_epoch UInt32 DEFAULT 0;
