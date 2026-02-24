BIN := pmux
GO := go

.PHONY: build clean run

## build: compile the binary
build:
	$(GO) build -o $(BIN) .

## run: build and run with arguments (usage: make run ARGS='run "bun dev"')
run: build
	./$(BIN) $(ARGS)

## clean: remove build artifacts
clean:
	rm -f $(BIN)

## dev: build in watch mode using fswatch (requires fswatch)
dev:
	@echo "watching for changes..."
	@$(MAKE) build
	@fswatch -o --exclude '$(BIN)' -e '.*' -i '\\.go$$' . | while read; do \
		echo "rebuilding..."; \
		$(MAKE) build; \
	done

## cross: cross-compile for all targets
cross:
	GOOS=darwin GOARCH=amd64 $(GO) build -o $(BIN)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 $(GO) build -o $(BIN)-darwin-arm64 .
	GOOS=linux  GOARCH=amd64 $(GO) build -o $(BIN)-linux-amd64  .
	GOOS=linux  GOARCH=arm64 $(GO) build -o $(BIN)-linux-arm64  .

## help: show this help
help:
	@grep -E '^## ' Makefile | sed 's/## //' | column -t -s ':'
