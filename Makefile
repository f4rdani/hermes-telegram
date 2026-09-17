.PHONY: all build clean test run

BINARY_NAME=hermes-tele

all: build

build:
	go build -ldflags="-s -w" -o $(BINARY_NAME) ./cmd/hermes-tele

run: build
	./$(BINARY_NAME) -config config.json

clean:
	rm -f $(BINARY_NAME)
