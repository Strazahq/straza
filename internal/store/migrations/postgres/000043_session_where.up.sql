-- host is the name of the machine the session's straza service runs on, and
-- folder is the folder the session started in, with the home directory
-- written as ~. The client names both at the check-in that starts the
-- session, and the server keeps them as sent. The console shows them on
-- Sessions. A session started before these columns, or by a client that
-- sent neither, keeps them empty. See the sqlite twin.
ALTER TABLE sessions ADD COLUMN host text DEFAULT ''::text NOT NULL;
ALTER TABLE sessions ADD COLUMN folder text DEFAULT ''::text NOT NULL;
