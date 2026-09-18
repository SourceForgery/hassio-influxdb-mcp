.PHONY: test test-race vet fmt-check build validate

test:
	cd influxdb_mcp && go test ./...

test-race:
	cd influxdb_mcp && go test -race ./...

vet:
	cd influxdb_mcp && go vet ./...

fmt-check:
	@files="$$(cd influxdb_mcp && gofmt -l .)"; \
	if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

build:
	cd influxdb_mcp && CGO_ENABLED=0 go build -buildvcs=false -trimpath ./cmd/ha-influxdb-mcp

validate:
	python3 scripts/validate_repository.py
