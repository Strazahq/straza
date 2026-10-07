# The eval stack's demo upstream, primed at build time: the MCP reference
# server is installed at a pinned version, so a cold start runs the installed
# binary with no registry fetch and no npm audit step in front of it. Same
# transport and port as the apps manifest expects: streamable HTTP on
# :3001/mcp, the package's own default.
FROM node:22-alpine
RUN npm install -g --no-audit --no-fund @modelcontextprotocol/server-everything@2026.8.31
CMD ["mcp-server-everything", "streamableHttp"]
