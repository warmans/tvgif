LOCAL_BIN ?= ./.env

.PHONY: install.golangci
install.golangci:
	mkdir -p $(LOCAL_BIN) && curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(LOCAL_BIN) v1.56.2

.PHONY: build
build:
	go build -o ./bin/tvgif

.PHONY: validate-srts
validate-srts: build
ifndef MEDIA_PATH
	$(error "MEDIA_PATH was not defined in environment")
endif
	./bin/tvgif importer validate-srt $(MEDIA_PATH)

.PHONY: recreate-meta
recreate-meta: build
ifndef MEDIA_PATH
	$(error "MEDIA_PATH was not defined in environment")
endif
	./bin/tvgif importer srt --clean=true $(MEDIA_PATH)

.PHONY: update-meta
update-meta: build
ifndef MEDIA_PATH
	$(error "MEDIA_PATH was not defined in environment")
endif
	./bin/tvgif importer srt --clean=false $(MEDIA_PATH)

.PHONY: refresh
refresh: update-meta
	./bin/tvgif importer refresh-index && ./bin/tvgif importer refresh-db

.PHONY: lint
lint:
	./.env/golangci-lint run
