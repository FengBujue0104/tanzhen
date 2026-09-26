FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache bash git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# scripts/build.sh stamps this into every binary (-X .../agent.Version).
ARG VERSION=dev
# BuildKit sets TARGETARCH to the image platform (amd64 or arm64). The
# classic builder leaves it empty, so the container runs the linux/amd64
# hub. Both hub binaries are always copied into /app/releases below.
ARG TARGETARCH
ENV VERSION=${VERSION}
# Same artifact set as scripts/build.sh: hub linux/amd64+arm64 and every
# agent the installers can ask for. The process we exec is the hub that
# matches this image's architecture.
RUN mkdir -p /out \
 && ./scripts/build.sh \
 && arch="${TARGETARCH:-amd64}" \
 && case "$arch" in \
      amd64|arm64) ;; \
      *) echo "tanzhen hub image supports linux/amd64 and linux/arm64, not $arch" >&2; exit 1 ;; \
    esac \
 && cp "releases/tanzhen-hub-linux-${arch}" /out/tanzhen-hub \
 && cp -a releases /out/releases

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/tanzhen-hub /app/tanzhen-hub
# Both hub binaries ride along so a hub deployed from another hub's
# /install-hub.sh can mirror either arch instead of reaching GitHub.
COPY --from=build /out/releases /app/releases

# No ADMIN_PASSWORD here on purpose: the hub refuses to boot on the built-in
# default, so an operator who forgets to set one gets a loud failure instead of
# a silently unauthenticated admin API.
ENV PORT=8080 \
    DATA_DIR=/data \
    RELEASES_DIR=/app/releases \
    ADMIN_USER=admin \
    TZ=Asia/Hong_Kong

VOLUME ["/data"]
EXPOSE 8080
CMD ["/app/tanzhen-hub"]
