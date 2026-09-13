GO ?= go
ARGS ?=

.DEFAULT_GOAL := build
.DELETE_ON_ERROR:

.PHONY: build run install fmt vet test test-race check tidy clean help

build:
	$(GO) build -o bin/ah ./cmd/ah

run: build
	./bin/ah $(ARGS)

install:
	$(GO) install ./cmd/ah

fmt:
	gofmt -w cmd internal

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

check: build vet test

tidy:
	$(GO) mod tidy

clean:
	rm -f bin/ah

help:
	@printf '%s\n' \
	  'make             构建 bin/ah（默认目标）' \
	  'make run ARGS="c A"  构建并运行，ARGS 指定命令参数' \
	  'make install     安装到 GOBIN 或 GOPATH/bin' \
	  'make fmt         格式化 Go 代码' \
	  'make vet         运行静态检查' \
	  'make test        运行全部测试' \
	  'make test-race   运行竞态检测测试' \
	  'make check       构建、静态检查和测试' \
	  'make tidy        整理模块依赖' \
	  'make clean       删除 bin/ah 构建产物'
