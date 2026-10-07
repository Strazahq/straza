# Runner image for the agent-e2e lane: the repo's CURRENT straza binary
# (built from source, CGO off) + the stdlib-only python agentkit + the driver.
# Build context is the repo root (see ../compose.yaml).
ARG GO_VERSION=1.25
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -o /out/straza ./cmd/straza

FROM python:3.12-slim
COPY --from=build /out/straza /usr/local/bin/straza
# The stdlib python kit (kits/python) locates the binary via $STRAZA_BIN (its
# lookup contract) then which("straza"); the binary is already on PATH, and
# STRAZA_BIN pins it explicitly.
ENV STRAZA_BIN=/usr/local/bin/straza
# straza-agentkit base install has zero dependencies (stdlib-only by design).
COPY kits/python /opt/straza-agentkit
RUN pip install --no-cache-dir /opt/straza-agentkit
COPY test/agent-e2e/runner/driver.py /opt/e2e/driver.py
COPY test/agent-e2e/policy /opt/e2e/policy
# /work is the episode sandbox the model's file/shell tools are jailed to.
WORKDIR /work
ENTRYPOINT ["python3", "-u", "/opt/e2e/driver.py"]
