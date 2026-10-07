# Support

Straza is maintained by one person with help from its contributors, so the channels
below are the ones that get read.

## Where to ask

A bug or a feature request goes to [GitHub issues](https://github.com/strazahq/straza/issues)
on strazahq/straza. Search the open issues first, because someone may have hit the same
thing.

Anything else goes to hello@straza.ai: a question about running Straza, a deployment or
integration engagement, or a commercial license.

A vulnerability goes to security@straza.ai or through GitHub private vulnerability
reporting, never to a public issue. [SECURITY.md](SECURITY.md) has the response times
and the scope.

## What to include

For a server problem, send the output of `strazad version`, the profile you run
(standalone or enterprise), and the exact error text or log line, with any password or
token masked.

For a client problem, send the output of `straza doctor` on the affected machine. It
reports the enrollment, the identity, the server and the approver surface, and each
warning it prints is followed by the command that fixes it. Add the exact error text
the agent or the command showed you.

For a console problem, name the browser and the page, and paste the error text as it
appeared.

The documentation is at https://docs.straza.ai/, and its
[reference](https://docs.straza.ai/reference/) section lists every command and
configuration key.
