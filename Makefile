BINARY := monkeys-compute-node-agent
MAIN := ./cmd/monkeys-compute-node-agent
DIST := dist

.PHONY: test build build-linux verify-dist container-build container-test clean

test:
	go test ./...
	sh scripts/install_test.sh

build:
	go build -trimpath -o bin/$(BINARY) $(MAIN)

build-linux:
	go run ./tools/build-linux

verify-dist: build-linux
	cd $(DIST) && if command -v sha256sum >/dev/null 2>&1; then sha256sum -c SHA256SUMS; else shasum -a 256 -c SHA256SUMS; fi
	test -x $(DIST)/install.sh

container-build:
	docker build --build-arg VERSION="$${VERSION:-dev}" -t monkeys-compute-node-agent:local .

container-test:
	sh scripts/container_test.sh

clean:
	rm -rf bin $(DIST)
