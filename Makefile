.PHONY: all build server client test vet tidy fmt clean

all: vet test build

build: server client

server:
	go build -o bin/claude-share-server ./cmd/server

client:
	go build -o bin/claude-share-client ./cmd/client

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

fmt:
	gofmt -w .

clean:
	rm -rf bin
