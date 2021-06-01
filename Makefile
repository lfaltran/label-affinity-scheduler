SHELL = /bin/bash
OS = $(shell uname -s)

# Project variables
PACKAGE = github.com/lfaltran/label-affinity-scheduler
BINARY_NAME = scheduler
IMAGE = lfaltran/label-affinity-scheduler
TAG = v0.2

# Build variables
BUILD_DIR ?= build
BUILD_PACKAGE = ${PACKAGE}/scheduler
VERSION ?= $(shell git rev-parse --abbrev-ref HEAD)
COMMIT_HASH ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BUILD_DATE ?= $(shell date +%FT%T%z)
LDFLAGS += -X main.Version=${VERSION} -X main.CommitHash=${COMMIT_HASH} -X main.BuildDate=${BUILD_DATE}

export CGO_ENABLED ?= 0

GOLANG_VERSION = 1.16

.PHONY: build
build: GOARGS += -tags "${GOTAGS}" -ldflags "${LDFLAGS}" -o ${BUILD_DIR}/${BINARY_NAME}
build: ## Build a binary

	go mod init
	go mod tidy
	go build ${GOARGS} ${BUILD_PACKAGE}