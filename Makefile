.PHONY: tidy test build hub agent cross run-hub clean

tidy:
	go mod tidy

test:
	go test ./...

hub:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o dist/tanzhen-hub ./cmd/hub

agent:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o dist/tanzhen-agent ./cmd/agent

build: hub agent

cross:
	./scripts/build.sh

run-hub: hub
	ADMIN_TOKEN?=changeme
	DATA_DIR?=./data
	PORT?=8080
	PUBLIC_URL?=http://127.0.0.1:$(PORT)
	RELEASES_DIR?=./releases
	ADMIN_TOKEN=$(ADMIN_TOKEN) DATA_DIR=$(DATA_DIR) PORT=$(PORT) PUBLIC_URL=$(PUBLIC_URL) RELEASES_DIR=$(RELEASES_DIR) ./dist/tanzhen-hub

clean:
	rm -rf dist/ releases/ data/
