# The eval stack's MCP Apps upstream: the official basic example server of the
# MCP Apps extension, installed at a pinned version so a cold start needs no
# registry fetch. One tool, get-time, opens the view ui://get-time/mcp-app.html,
# and the view's button calls get-time again by its own name. Streamable HTTP
# on :3001/mcp, the package's own default. MIT licensed.
FROM node:22-alpine
RUN npm install -g --no-audit --no-fund @modelcontextprotocol/server-basic-vanillajs@2.0.3
ENV PORT=3001
CMD ["mcp-server-basic-vanillajs"]
