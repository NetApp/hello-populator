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

deploy/ca-certificates.crt:
	cp /etc/ssl/certs/ca-certificates.crt deploy/

build: compile deploy/ca-certificates.crt
	docker build -f deploy/Dockerfile -t $(TAG) .

clean:
	go clean -i -x ./...

.PHONY: all compile build clean
