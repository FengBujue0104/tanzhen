.PHONY: tidy test vet build hub agent cross run-hub clean

tidy:
	go mod tidy

test:
	go test ./...

vet:
	go vet ./...

build: hub agent

hub:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o dist/tanzhen-hub ./cmd/hub

agent:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o dist/tanzhen-agent ./cmd/agent

cross:
	./scripts/build.sh

# Local run. ADMIN_PASSWORD is required — set it in your shell or override here:
#   make run-hub ADMIN_PASSWORD=hunter2
run-hub: hub
	ADMIN_PASSWORD?=changeme
	ALLOW_DEFAULT_PASSWORD?=0
	DATA_DIR?=./data
	PORT?=8080
	PUBLIC_URL?=http://127.0.0.1:$(PORT)
	RELEASES_DIR?=./releases
	ADMIN_PASSWORD=$(ADMIN_PASSWORD) ALLOW_DEFAULT_PASSWORD=$(ALLOW_DEFAULT_PASSWORD) \
	  DATA_DIR=$(DATA_DIR) PORT=$(PORT) PUBLIC_URL=$(PUBLIC_URL) RELEASES_DIR=$(RELEASES_DIR) \
	  ./dist/tanzhen-hub

clean:
	rm -rf dist/ releases/ data/
