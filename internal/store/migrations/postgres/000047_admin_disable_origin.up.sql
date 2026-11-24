-- A disable in Straza writes its revocation with the origin admin-disable,
-- so an admin enable can lift it and leave a lock. Rows written before
-- carry the origin admin and the server's own reason. A lock whose typed
-- reason is exactly that sentence is re-tagged too, and reads as a disable.
UPDATE revocations SET origin = 'admin-disable'
    WHERE kind = 'user' AND origin = 'admin' AND reason = 'user disabled by admin';
