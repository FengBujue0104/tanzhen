FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /out/tanzhen-hub ./cmd/hub \
 && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /out/releases/tanzhen-agent-linux-amd64 ./cmd/agent \
 && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o /out/releases/tanzhen-agent-linux-arm64 ./cmd/agent \
 && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o /out/releases/tanzhen-agent-windows-amd64.exe ./cmd/agent

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/tanzhen-hub /app/tanzhen-hub
COPY --from=build /out/releases /app/releases
ENV PORT=8080 \
    DATA_DIR=/data \
    RELEASES_DIR=/app/releases \
    ADMIN_TOKEN=changeme \
    TZ=Asia/Hong_Kong
VOLUME ["/data"]
EXPOSE 8080
CMD ["/app/tanzhen-hub"]
