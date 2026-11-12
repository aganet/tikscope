VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build run test audit docker up down logs clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o tikscope ./cmd/tikscope

run: build
	./tikscope

test:
	go vet ./...
	go test -race ./...
	@for f in web/js/*.js web/js/views/*.js; do node --check $$f || exit 1; done

audit:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	trivy fs --scanners vuln --severity HIGH,CRITICAL --exit-code 1 .

docker:
	docker build --build-arg VERSION=$(VERSION) -t tikscope:$(VERSION) -t tikscope:latest .

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f

clean:
	rm -f tikscope
