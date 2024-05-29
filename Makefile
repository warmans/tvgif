.PHONY: build
build:
	go build -o ./bin/tvgif

.PHONY: refresh
refresh: build
	./bin/tvgif importer refresh-index

.PHONY: run.discord-bot
run.discord-bot:
	DEBUG=true ./bin/tvgif bot
