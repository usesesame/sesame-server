-- Sesame drops the paid desktop tier. The desktop app is AGPL software that
-- costs nothing, so a one-time "Founding Pro" licence no longer exists and its
-- row is removed rather than left disabled where staff could re-enable it.
--
-- Sync becomes the only paid plan. It stays unavailable until its security
-- review passes; this migration sets a price, not a launch.
--
-- The yearly option was previously described in prose because the table had
-- nowhere to put it, so a database-backed /v1/plans could not serve the price
-- the website rendered. The column fixes that.

ALTER TABLE sesame_product_plans ADD COLUMN IF NOT EXISTS annual_price TEXT;

DELETE FROM sesame_product_plans WHERE id = 'founding-pro';

UPDATE sesame_product_plans
SET name = 'Sesame',
    price = '0',
    billing = 'none',
    description = 'The whole app, free and open source under the AGPL.',
    available = TRUE,
    includes = '["Encrypted vault","15 import formats","Nine record types","2FA and recovery details","Windows Hello and PIN unlock","Backup, restore, and export"]',
    updated_at = NOW()
WHERE id = 'free';

UPDATE sesame_product_plans
SET name = 'Sesame Sync',
    price = '1.00',
    annual_price = '10.00',
    billing = 'monthly',
    description = 'Optional hosted sync between your own approved devices. Not available until its security review passes.',
    available = FALSE,
    includes = '["Approved devices","End-to-end encryption","Conflict review","Local access if Sync ends","Self-host it instead if you prefer"]',
    updated_at = NOW()
WHERE id = 'sync';
