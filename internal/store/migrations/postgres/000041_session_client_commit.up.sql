-- client_commit is the git commit of the straza build that started the
-- session, as the client named it at check-in, cut to 64 bytes. The
-- console compares it with the server's own commit, because the two build
-- paths spell the version string differently. A session started before
-- this column, or by a client that sent no commit, keeps it empty. See the
-- sqlite twin.
ALTER TABLE sessions ADD COLUMN client_commit text DEFAULT ''::text NOT NULL;
