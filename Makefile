.PHONY: build test test-race test-integration lint benchmark seed simulate up down clean frontend generate acceptance security
build:
	go build -o bin/ ./cmd/...
test:
	go test ./...
test-race:
	go test -race ./...
test-integration:
	sh scripts/test-integration.sh
lint:
	test -z "$$(gofmt -l cmd internal migrations tests api/gen)"
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
benchmark:
	sh scripts/benchmark-matrix.sh
seed:
	docker compose run --rm seed
simulate:
	docker compose run --rm simulator --url http://gateway:8080 --run demo-1
up:
	docker compose up --build -d
down:
	docker compose down
clean:
	go clean -cache -testcache
frontend:
	cd web && npm ci && npm run build && npm test
generate:
	sh scripts/generate.sh
acceptance:
	sh scripts/acceptance.sh
security:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
	cd web && npm audit --audit-level=high
