VERSION ?= 0.1.0
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build install check test test-examples clean

build:
	go build -ldflags "$(LDFLAGS)" -o jen .

install:
	go install -ldflags "$(LDFLAGS)" .

check:
	go vet ./...
	go build ./...

test:
	go test ./...

test-examples:
	elixir examples/word_stats_test.exs

clean:
	rm -f jen
