GO          ?= go
GOFMT       ?= $(GO)fmt
GOOS        ?= $(shell $(GO) env GOOS)
GOARCH      ?= $(shell $(GO) env GOARCH)
GO_VERSION  ?= $(shell $(GO) version)
GO111MODULE :=
pkgs         = ./...

APP_NAME        ?= demoapp
BUILD_VERSION   ?= $(shell cat VERSION)
BUILD_DATE      ?= $(shell date +"%Y%m%d-%T")
BUILD_BRANCH    ?= $(shell git rev-parse --abbrev-ref HEAD)
BUILD_REVERSION ?= $(shell git rev-parse HEAD)

all: style vet test build

.PHONY: style
style:
	@echo ">> checking code style"
	@fmtRes=$$($(GOFMT) -d $$(find . -path ./vendor -prune -o -name '*.go' -print)); \
	if [ -n "$${fmtRes}" ]; then \
		echo "gofmt checking failed!"; echo "$${fmtRes}"; echo; \
		echo "Please ensure you are using $$($(GO) version) for formatting code."; \
		exit 1; \
	fi
	
.PHONT: test
test:
	@echo ">> running all tests"
	CGO_ENABLED=1 GO111MODULE=$(GO111MODULE) $(GO) test -race -cover $(pkgs)

.PHONY: vet
vet:
	@echo ">> vetting code"
	GO111MODULE=$(GO111MODULE) $(GO) vet $(pkgs)

.PHONY: build
build:
	@echo ">> building code" 
	CGO_ENABLED=0 GO111MODULE=$(GO111MODULE) $(GO) build -ldflags="-s -w \
	-X github.com/prometheus/common/version.Version=$(BUILD_VERSION) \
	-X github.com/prometheus/common/version.BuildDate=$(BUILD_DATE) \
	-X github.com/prometheus/common/version.Branch=$(BUILD_BRANCH) \
	-X github.com/prometheus/common/version.Revision=$(BUILD_REVERSION) \
	-X github.com/prometheus/common/version.BuildUser=ilolicon" \
	-o ./build/$(GOOS)-$(GOARCH)/$(APP_NAME) ./cmd/$(APP_NAME)/main.go

.PHONY: docker
docker:
	@echo ">> building code"
	@docker build --build-arg ARCH=$(GOARCH) --build-arg OS=$(GOOS) -t ilolicon/$(APP_NAME):$(BUILD_VERSION) .
	@docker push ilolicon/$(APP_NAME):$(BUILD_VERSION)
