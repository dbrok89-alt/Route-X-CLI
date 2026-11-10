GO ?= go
BIN := route-x

.PHONY: build test vet install clean

build:
	$(GO) build -o $(BIN) .

test:
	$(GO) vet ./...
	$(GO) test -race ./...

fmt:
	test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

vet:
	$(GO) vet ./...

install: build
	install -m 755 $(BIN) /usr/local/bin/$(BIN)

clean:
	rm -f $(BIN)
