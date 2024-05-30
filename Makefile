LOCAL_BIN ?= ./.env

.PHONY: install.golangci
install.golangci:
	mkdir -p $(LOCAL_BIN) && curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(LOCAL_BIN) v1.56.2

.PHONY: build
build:
	go build -o ./bin/tvgif

.PHONY: refresh
refresh: build
	./bin/tvgif importer refresh-index

.PHONY: lint
lint:
	./.env/golangci-lint run
