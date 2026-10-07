# syntax=docker/dockerfile:1
# Demo service image: the governed-agents showcase built from a
# straza-agents-demo checkout, consuming the python kit exactly as a user
# would. Build contexts (wired in compose.demo.yaml): `straza` = this repo's
# root (the straza client binary and the kit), `demo-src` = the
# straza-agents-demo checkout. A missing checkout fails the build loudly at
# context preparation, by design.

ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-bookworm AS straza-build
WORKDIR /src

COPY --from=straza go.mod go.sum ./
RUN go mod download

COPY --from=straza . .
ARG VERSION=dev
ARG COMMIT=none
ENV CGO_ENABLED=0
RUN go build -trimpath \
      -ldflags "-s -w -X github.com/strazahq/straza/internal/version.Version=${VERSION} -X github.com/strazahq/straza/internal/version.Commit=${COMMIT}" \
      -o /out/straza ./cmd/straza

FROM python:3.12-slim
# Kit first (path install: the demo's straza-agentkit dependency is then
# already satisfied), demo second. [live] = langchain-anthropic, and its key
# only ever arrives via env at run time.
COPY --from=straza kits/python /opt/straza-kit
COPY --from=demo-src . /opt/straza-agents-demo
RUN pip install --no-cache-dir "/opt/straza-kit[langchain]" \
 && pip install --no-cache-dir "/opt/straza-agents-demo[live]"

# Tier-1 hook binary for the demo's hook-lane scenarios; the kit resolves
# $STRAZA_BIN before PATH.
COPY --from=straza-build /out/straza /usr/local/bin/straza
ENV STRAZA_BIN=/usr/local/bin/straza

# Non-root; the checkout is handed to the runtime user because record mode
# writes replays/ under the working tree.
RUN useradd --create-home --uid 65532 demo \
 && chown -R demo:demo /opt/straza-agents-demo
USER demo
WORKDIR /opt/straza-agents-demo

# The chat face binds 127.0.0.1:8477 by construction (no widen flag exists);
# under network_mode: host that IS the eval host's loopback. Same exposure
# posture as the loopback overlay: reach it through an SSH tunnel.
ENTRYPOINT ["demo", "chat"]
