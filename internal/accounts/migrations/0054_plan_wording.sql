UPDATE sesame_product_plans
SET description = 'The whole app, open source under the AGPL. There is no paid version.',
    includes = '["Encrypted local vault","Import from 15 formats","Nine item types","2FA codes and recovery details","Windows Hello and PIN unlock","Backup, restore, and export","Sync with your own server"]',
    updated_at = NOW()
WHERE id = 'free';

UPDATE sesame_product_plans
SET description = 'Keeps your vault the same on every device you approve. We run the server, so you do not have to.',
    includes = '["Sync server run by us","End-to-end encryption","Device approval","Conflict review","Local access if you cancel"]',
    updated_at = NOW()
WHERE id = 'sync';
