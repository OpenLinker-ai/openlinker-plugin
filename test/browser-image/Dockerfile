# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

ARG GO_VERSION=1.26.4
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm@sha256:b305420a68d0f229d91eb3b3ed9e519fcf2cf5461da4bef997bf927e8c0bfd2b AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly GOWORK=off
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY packages/browser-runtime ./packages/browser-runtime
COPY packages/agent-adapters ./packages/agent-adapters
COPY test/browser-image ./test/browser-image
RUN go test ./packages/browser-runtime/browserclient ./packages/browser-runtime/browserplugin ./packages/browser-runtime/browserprotocol ./packages/browser-runtime/netpolicy \
 && go test ./test/browser-image/client ./test/browser-image/fixture
ARG TARGETOS
ARG TARGETARCH
RUN test "${TARGETOS}" = linux \
 && GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build -trimpath -ldflags="-s -w" -o /out/browser-image-client ./test/browser-image/client \
 && GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build -trimpath -ldflags="-s -w" -o /out/browser-image-fixture ./test/browser-image/fixture

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35 AS client
COPY --from=build /out/browser-image-client /usr/local/bin/browser-image-client
ENTRYPOINT ["/usr/local/bin/browser-image-client"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35 AS fixture
COPY --from=build /out/browser-image-fixture /usr/local/bin/browser-image-fixture
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/browser-image-fixture"]

FROM node:24.4.1-bookworm-slim@sha256:36ae19f59c91f3303c7a648f07493fe14c4bd91320ac8d898416327bacf1bbfa AS observer
RUN apt-get update \
 && apt-get install -y --no-install-recommends tcpdump \
 && rm -rf /var/lib/apt/lists/*
ENTRYPOINT ["/usr/bin/tcpdump"]
