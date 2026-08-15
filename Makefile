

.PHONY: fsnotify-daemon fsnotify-proxy fmt vet test test-integration

# Host port for the throwaway redis used by test-integration.
REDIS_PORT ?= 16379

all: fsnotify-daemon fsnotify-proxy

tidy: 
	go mod tidy
	
fmt: ;$(info $(M)...Begin to run go fmt against code.) @
	go fmt ./...

vet: ;$(info $(M)...Begin to run go vet against code.) @
	go vet ./...

# Scoped to ./k8s/... because the jfsnotify tests depend on prebuilt /data fixtures.
test: ;$(info $(M)...Run unit and socket integration tests.) @
	go test ./k8s/... -race -count=1

test-integration: ;$(info $(M)...Run redis-backed integration tests.) @
	@docker rm -f fs-lib-redis >/dev/null 2>&1 || true
	docker run --rm -d -p $(REDIS_PORT):6379 --name fs-lib-redis redis:7-alpine
	@for i in $$(seq 1 50); do \
		docker exec fs-lib-redis redis-cli ping >/dev/null 2>&1 && break; \
		sleep 0.2; \
	done
	@REDIS_ADDR=127.0.0.1:$(REDIS_PORT) go test ./k8s/... -tags integration -race -count=1; \
		status=$$?; docker rm -f fs-lib-redis >/dev/null; exit $$status

fsnotify-daemon: fmt vet ;$(info $(M)...Begin to build fsnotify-daemon.) @
	go build -o output/fsnotify-daemon ./k8s/cmd/fsnotify-daemon/main.go

linux-daemon: fmt vet ;$(info $(M)...Begin to build fsnotify-daemon - linux version.) @
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC=x86_64-linux-musl-gcc CGO_LDFLAGS="-static" go build -a -o output/fsnotify-daemon ./k8s/cmd/fsnotify-daemon/main.go

run-daemon: fmt vet ; $(info $(M)...Run fsnotify-daemon.)
	go run  ./k8s/cmd/fsnotify-daemon/main.go -v 4

fsnotify-proxy: fmt vet ;$(info $(M)...Begin to build fsnotify-proxy.) @
	go build -o output/fsnotify-proxy ./k8s/cmd/fsnotify-proxy/main.go

linux-proxy: fmt vet ;$(info $(M)...Begin to build fsnotify-proxy - linux version.) @
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC=x86_64-linux-musl-gcc CGO_LDFLAGS="-static" go build -a -o output/fsnotify-proxy ./k8s/cmd/fsnotify-proxy/main.go

run-proxy: fmt vet ; $(info $(M)...Run fsnotify-proxy.)
	go run  ./k8s/cmd/fsnotify-proxy/main.go -v 4

update-codegen: ## generetor clientset informer inderx code
	./hack/update-codegen.sh