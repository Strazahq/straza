# Agent pod image: the repo's CURRENT straza client (built from source, CGO
# off) + the stdlib python agentkit, running as the midPoint-born NHI
# `sam-sre-agent`. Build context is the repo root (see ../compose.yaml). It
# mirrors test/agent-e2e/runner/Dockerfile. The same image runs sam's straza
# service in its own container under the straza-service account.
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
# curl + jq: the entrypoint's gateway probe is shell, kept legible for
# evaluators tailing the logs.
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
# straza-service (uid 64000) runs sam's service container. Its passwd line
# lets the straza client check that the socket belongs to the service, and
# Docker copies these two directories' owner and mode into the empty socket
# and state volumes the first time a container mounts them.
RUN groupadd --system --gid 64000 straza-service \
  && useradd --system --uid 64000 --gid 64000 --no-create-home \
       --home-dir /var/lib/straza-service --shell /usr/sbin/nologin straza-service \
  && install -d -o 64000 -g 64000 -m 0755 /run/straza-service \
  && install -d -o 64000 -g 64000 -m 0700 /var/lib/straza-service
# /work is sam's episode sandbox; ./deploy is the gated demo command
# (agent-guardrails deploy-ticket matches "./deploy*").
COPY --chown=sam:sam deploy/compose/eval-stack/agent-sam/deploy /work/deploy
RUN chmod 0755 /work/deploy
USER sam
WORKDIR /work
ENTRYPOINT ["sh", "/opt/sam/entrypoint.sh"]
