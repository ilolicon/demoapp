SHELL := /bin/bash # Use bash syntax

GO          ?= go
GOFMT       ?= $(GO)fmt
GOOS        ?= $(shell $(GO) env GOOS)
GOARCH      ?= $(shell $(GO) env GOARCH)
GO_VERSION  ?= $(shell $(GO) version)
GO111MODULE :=
pkgs         = ./...

APP_NAME        ?= demoapp
BUILD_VERSION   ?= $(shell cat VERSION)

all: style vet test build docker

.PHONY: style
style:
	@echo ">> checking code style"
	@fmtRes=$$($(GOFMT) -d $$(find . -path ./vendor -prune -o -name '*.go' -print)); \
	if [[ -n "$${fmtRes}" ]]; then \
		echo "gofmt checking failed!"; echo "$${fmtRes}"; echo; \
		echo "Please ensure you are using $$($(GO) version) for formatting code."; \
		exit 1; \
	fi
	
.PHONT: test
test:
	@echo ">> running all tests"
	GO111MODULE=$(GO111MODULE) $(GO) test -race -cover $(pkgs)

.PHONY: vet
vet:
	@echo ">> vetting code"
	GO111MODULE=$(GO111MODULE) $(GO) vet $(pkgs)

.PHONY: build
build:
	@echo ">> building code" 
	CGO_ENABLED=0 GO111MODULE=$(GO111MODULE) $(GO) build -ldflags="-s -w \
	-X main.AppName=$(APP_NAME) \
	-X main.Version=$(BUILD_VERSION)" \
	-o ./build/$(GOOS)-$(GOARCH)/$(APP_NAME) ./main.go

.PHONY: docker
docker:
	@echo ">> building code"
	@docker build --build-arg ARCH=$(GOARCH) --build-arg OS=$(GOOS) -t ilolicon/$(APP_NAME):$(BUILD_VERSION) .
	@docker push ilolicon/$(APP_NAME):$(BUILD_VERSION)
