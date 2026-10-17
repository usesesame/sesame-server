DROP INDEX sesame_sync3_devices_token_key;
CREATE UNIQUE INDEX sesame_sync3_devices_token_key
  ON sesame_sync3_devices (vault_id, token_device_id) WHERE state <> 'revoked';
