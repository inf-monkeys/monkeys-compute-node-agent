BINARY := monkeys-compute-node-agent
MAIN := ./cmd/monkeys-compute-node-agent
DIST := dist

.PHONY: test build build-linux clean

test:
	go test ./...

build:
	go build -o bin/$(BINARY) $(MAIN)

build-linux:
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(DIST)/$(BINARY)_linux_amd64 $(MAIN)
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(DIST)/$(BINARY)_linux_arm64 $(MAIN)

clean:
	rm -rf bin $(DIST)
