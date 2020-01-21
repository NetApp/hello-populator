#
# Copyright (c) 2020 NetApp
# All rights reserved
#

export CGO_ENABLED=0
export GOOS=linux
export GOARCH=amd64
IMAGE_VERSION?=canary
TAG=quay.io/k8scsi/hello-populator:$(IMAGE_VERSION)

REV=$(shell git describe --long --tags --match='v*' --dirty)

all: build

compile:
	mkdir -p bin
	go build -ldflags '-s -w -X main.version=$(REV)' -o bin/hello-populator *.go

build: compile
	deploy/build.sh $(TAG)

clean:
	go clean -i -x ./...

.PHONY: all compile build clean
