# Multi-stage build for strazad + strazactl. CGO stays off:
# pure-Go sqlite and embedded NATS mean the runtime image needs no libc, so
# the final stage is scratch with just the static binaries, the CA certs and
# the license notices under /licenses/.
# The build stage runs on the BUILD platform and cross-compiles for TARGET;
# multi-arch buildx never compiles Go under qemu emulation.
ARG GO_VERSION=1.26
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0
# The notices of every Go module the binaries link and of the console bundle.
# They are written here because .dockerignore leaves out build/.
RUN go run ./tools/notices -release -version "${VERSION}" -o /out/THIRD_PARTY_NOTICES.txt
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X github.com/strazahq/straza/internal/version.Version=${VERSION} -X github.com/strazahq/straza/internal/version.Commit=${COMMIT}" \
      -o /out/strazad ./cmd/strazad \
 && GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X github.com/strazahq/straza/internal/version.Version=${VERSION} -X github.com/strazahq/straza/internal/version.Commit=${COMMIT}" \
      -o /out/strazactl ./cmd/strazactl

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/strazad /usr/local/bin/strazad
COPY --from=build /out/strazactl /usr/local/bin/strazactl
COPY --from=build /out/THIRD_PARTY_NOTICES.txt /licenses/THIRD_PARTY_NOTICES.txt
# The CA bundle above comes from the build stage's ca-certificates package,
# whose Mozilla certificate data is MPL-2.0; its terms travel with it.
COPY --from=build /usr/share/doc/ca-certificates/copyright /licenses/ca-certificates.copyright
COPY LICENSE LICENSE-APACHE NOTICE /licenses/

# Non-root by default (uid 65532 = distroless "nonroot"). The data dir is a
# volume; enterprise deployments use Postgres + external NATS and keep it empty.
USER 65532:65532
VOLUME ["/var/lib/straza"]
ENV STRAZA_DATA_DIR=/var/lib/straza
EXPOSE 8420
ENTRYPOINT ["/usr/local/bin/strazad"]
CMD ["serve", "--profile", "enterprise", "--listen", "0.0.0.0:8420"]
