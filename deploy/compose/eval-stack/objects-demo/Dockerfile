# The eval stack's object-scope fixture, objects-demo, built from
# test/objectsdemo in this checkout. The build stage repeats the first steps
# of deploy/Dockerfile on the same base image, so the two builds share their
# base and module layers. Build context is the repo root (see ../compose.yaml).
ARG GO_VERSION=1.26
FROM --platform=$BUILDPLATFORM mirror.gcr.io/library/golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w" \
      -o /out/objectsdemo ./test/objectsdemo

# One static binary and nothing else. The repo list lives in /data, which
# compose mounts as a tmpfs, so the stack always starts from the ten repos.
FROM scratch
COPY --from=build /out/objectsdemo /usr/local/bin/objectsdemo
USER 65532:65532
EXPOSE 3002
ENTRYPOINT ["/usr/local/bin/objectsdemo"]
