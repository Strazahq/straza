-- An older server writes admin for a disable and lifts every row at an
-- enable, so each admin-disable row goes back to admin. Every such row
-- carries the reason user disabled by admin, so the older console still
-- reads it as a disable.
UPDATE revocations SET origin = 'admin'
    WHERE kind = 'user' AND origin = 'admin-disable';
