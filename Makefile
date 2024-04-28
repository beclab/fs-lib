

.PHONY: fsnotify-daemon fsnotify-proxy fmt vet

all: fsnotify-daemon fsnotify-proxy

tidy: 
	go mod tidy
	
fmt: ;$(info $(M)...Begin to run go fmt against code.) @
	go fmt ./...

vet: ;$(info $(M)...Begin to run go vet against code.) @
	go vet ./...

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