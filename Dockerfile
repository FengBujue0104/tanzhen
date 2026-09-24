FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# The agent version is stamped so a hub can show which build is reporting.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X github.com/FengBujue0104/tanzhen/internal/agent.Version=$VERSION" -o /out/tanzhen-hub ./cmd/hub \
 && for a in amd64 arm64 386; do \
      CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -ldflags="-s -w -X github.com/FengBujue0104/tanzhen/internal/agent.Version=$VERSION" -o /out/releases/tanzhen-agent-linux-$a ./cmd/agent; \
    done \
 && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X github.com/FengBujue0104/tanzhen/internal/agent.Version=$VERSION" -o /out/releases/tanzhen-agent-windows-amd64.exe ./cmd/agent \
 && CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags="-s -w -X github.com/FengBujue0104/tanzhen/internal/agent.Version=$VERSION" -o /out/releases/tanzhen-agent-windows-arm64.exe ./cmd/agent

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/tanzhen-hub /app/tanzhen-hub
# The hub binary rides along in /releases so a hub deployed from another hub's
# /install-hub.sh can mirror from this one instead of reaching GitHub.
COPY --from=build /out/tanzhen-hub /app/releases/tanzhen-hub-linux-amd64
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
