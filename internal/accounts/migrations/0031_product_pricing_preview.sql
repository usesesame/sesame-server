-- Keep the staff plan catalogue aligned with the public pricing preview.
-- Neither paid plan is enabled by this migration.
UPDATE sesame_product_plans
SET name = 'Local Vault',
    price = '0',
    billing = 'none',
    description = 'Your everyday password vault, with no subscription.',
    available = TRUE,
    includes = '["Encrypted vault","15 import formats","Nine record types","2FA and recovery details","Backup, restore, and export"]',
    updated_at = NOW()
WHERE id = 'free';

UPDATE sesame_product_plans
SET price = '20.00',
    billing = 'one_time',
    description = 'Pay once for the first set of Pro desktop tools.',
    available = FALSE,
    includes = '["Multiple vault profiles","Bulk cleanup tools","Backup health checks","All Pro updates in Sesame 1.x","12 months of Sync if it launches"]',
    updated_at = NOW()
WHERE id = 'founding-pro';

UPDATE sesame_product_plans
SET price = '2.50',
    billing = 'monthly',
    description = 'Optional encrypted Sync after independent review. A EUR 24 yearly option saves EUR 6.',
    available = FALSE,
    includes = '["Approved devices","End-to-end encryption","Conflict review","Local access if Sync ends"]',
    updated_at = NOW()
WHERE id = 'sync';
