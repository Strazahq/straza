# Agent pod image: the repo's CURRENT straza client (built from source, CGO
# off) + the stdlib python agentkit, running as the midPoint-born NHI
# `sam-sre-agent`. Build context is the repo root (see ../compose.yaml). It
# mirrors test/agent-e2e/runner/Dockerfile.
ARG GO_VERSION=1.26
FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ENV CGO_ENABLED=0
RUN go build -trimpath \
      -ldflags "-s -w -X github.com/strazahq/straza/internal/version.Version=${VERSION} -X github.com/strazahq/straza/internal/version.Commit=${COMMIT}" \
      -o /out/straza ./cmd/straza

FROM python:3.12-slim
# curl + jq: the entrypoint's gateway probe and session-token reads are shell,
# kept legible for evaluators tailing the logs.
RUN apt-get update && apt-get install -y --no-install-recommends curl jq ca-certificates \
  && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/straza /usr/local/bin/straza
ENV STRAZA_BIN=/usr/local/bin/straza
# straza-agentkit base install has zero dependencies (stdlib-only by design).
COPY kits/python /opt/straza-agentkit
RUN pip install --no-cache-dir /opt/straza-agentkit
COPY deploy/compose/eval-stack/agent-sam/entrypoint.sh \
     deploy/compose/eval-stack/agent-sam/raise-ticket.sh /opt/sam/
RUN useradd --create-home --uid 65532 sam \
  && mkdir -p /work && chown sam:sam /work \
  && chmod 0755 /opt/sam/*.sh
# /work is sam's episode sandbox; ./deploy is the gated demo command
# (agent-guardrails deploy-ticket matches "./deploy*").
COPY --chown=sam:sam deploy/compose/eval-stack/agent-sam/deploy /work/deploy
RUN chmod 0755 /work/deploy
USER sam
WORKDIR /work
ENTRYPOINT ["sh", "/opt/sam/entrypoint.sh"]
