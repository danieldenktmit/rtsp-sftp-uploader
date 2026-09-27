BINARY      := rtsp-sftp-uploader
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
IMAGE       ?= ghcr.io/danieldenktmit/rtsp-sftp-uploader
CHART       := charts/rtsp-sftp-uploader

HELM_MIN_SET := --set rtsp.host=cam.lan --set sftp.host=sftp.lan \
                --set sftp.username=up --set sftp.password=zzSECRETzz \
                --set sftp.insecureIgnoreHostKey=true

.PHONY: all build test test-race cover lint fmt tidy docker docker-smoke docker-multiarch helm-lint helm-test check-actions verify clean

all: verify build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/$(BINARY)

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

cover:
	./scripts/coverage.sh

lint:
	golangci-lint run ./...

fmt:
	gofmt -l -w .

tidy:
	go mod tidy
	git diff --exit-code go.mod go.sum

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t $(IMAGE):$(VERSION) .

docker-smoke: docker
	IMAGE=$(IMAGE):$(VERSION) ./scripts/docker-smoke.sh

docker-multiarch:
	docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 -t $(IMAGE):multiarch .

helm-lint:
	helm lint $(CHART) $(HELM_MIN_SET)
	helm template t $(CHART) $(HELM_MIN_SET) > /dev/null

helm-test:
	helm unittest $(CHART)

check-actions:
	./scripts/check-action-refs.sh

verify: lint test-race cover helm-lint helm-test check-actions

clean:
	rm -rf bin dist coverage.out coverage.internal.out coverage.html
