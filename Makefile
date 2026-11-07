BINARY  := switchctl
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PKG     := ./cmd/switchctl

.PHONY: build test race vet lint fmt fmt-check run clean check

build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/$(BINARY) $(PKG)

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

lint:
	@command -v staticcheck >/dev/null 2>&1 || { \
		echo "缺少 staticcheck：go install honnef.co/go/tools/cmd/staticcheck@latest"; exit 1; }
	staticcheck ./...

fmt:
	gofmt -l -w .

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "需要 gofmt："; echo "$$out"; exit 1; fi

run: build
	./bin/$(BINARY) -config configs/switchctl.yaml

clean:
	rm -rf bin

# 提交前的门禁：格式检查 + vet + staticcheck + 带竞态检测的测试
check: fmt-check vet lint race
