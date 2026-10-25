ALTER TABLE sesame_sync3_device_list
  ADD CONSTRAINT sesame_sync3_device_list_kind_check_pending
  CHECK (kind IN ('create', 'add', 'remove', 'key', 'rotate')) NOT VALID;
ALTER TABLE sesame_sync3_device_list
  DROP CONSTRAINT IF EXISTS sesame_sync3_device_list_kind_check;
ALTER TABLE sesame_sync3_device_list
  RENAME CONSTRAINT sesame_sync3_device_list_kind_check_pending
  TO sesame_sync3_device_list_kind_check;
