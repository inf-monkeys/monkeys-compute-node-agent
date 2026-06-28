BINARY := monkeys-compute-node-agent
MAIN := ./cmd/monkeys-compute-node-agent
DIST := dist

.PHONY: test build build-linux clean

test:
	go test ./...

build:
	go build -o bin/$(BINARY) $(MAIN)

build-linux:
	go run ./tools/build-linux

clean:
	rm -rf bin $(DIST)
