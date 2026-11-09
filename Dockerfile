# The program in a container.
#
#   docker build -t evmgraph .
#   docker run --name evmgraph -v evmgraph-data:/data evmgraph          generate or load, then wait
#   docker stop evmgraph                                                SIGTERM: stops cleanly
#   docker run --rm -v evmgraph-data:/data evmgraph balance 0x...
#
# The dataset lives in the volume, in /data/cache (the default -data, cache,
# relative to the working folder /data), so it survives the container, the
# next start loads it instead of generating it again, and every command finds
# it without -data.

# Build stage: the official Go image at the exact version go.mod asks for.
# It runs on the build machine's own architecture and cross-compiles for the
# target, so `docker buildx build --platform linux/amd64` on an arm64 Mac
# does not run the compiler under emulation.
FROM --platform=$BUILDPLATFORM golang:1.26.3-alpine3.23 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
# The module files first: this layer, with the downloaded dependency, is
# reused until they change, not rebuilt on every source edit.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# CGO_ENABLED=0 makes a static binary that needs no C library, so it runs on
# any Linux image; -trimpath keeps the build machine's paths out of it. The
# same command as `task build`.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /evmgraph ./cmd/evmgraph

# Run stage: Alpine, about 8 MB. The binary would also run on scratch or
# distroless, but Alpine has a shell and coreutils, so `docker exec` can look
# inside a running container and the container tests can fill its disk with dd.
FROM alpine:3.24
# A user without root rights: a fixed uid above the range of people's accounts,
# so it matches no one on the host. /data belongs to it, and Docker copies that
# ownership into a new named volume.
RUN addgroup -S -g 10001 evmgraph \
    && adduser -S -D -H -u 10001 -G evmgraph evmgraph \
    && mkdir /data && chown evmgraph:evmgraph /data
COPY --from=build /evmgraph /usr/local/bin/evmgraph
USER evmgraph
# The working folder is the volume, so the default -data (cache) and any
# relative one land in it, and nothing is written to the container's own
# layer, which is lost with it.
WORKDIR /data
VOLUME /data
# docker stop sends SIGTERM (the default, stated here because the program
# depends on it): the program folds in pending balance updates and exits 0.
# A signal while it generates stops the work at once, and one while it saves
# lets the save finish (about 4 s at 20M), both inside Docker's 10 s.
STOPSIGNAL SIGTERM
# The exec form makes evmgraph process 1 itself, with no shell in between, so
# it receives docker stop's SIGTERM. ENTRYPOINT is the program and CMD its
# default command: `docker run evmgraph` runs it, and `docker run evmgraph
# balance 0x...` replaces only the command.
ENTRYPOINT ["evmgraph"]
CMD ["run"]
