#!/bin/sh -xe

[ -n "$1" ] || exit 1

C=$(buildah from -q docker.io/library/busybox:1.31.1)
buildah add -q "$C" bin/hello-populator /
buildah add -q "$C" /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
buildah config --entrypoint '["/hello-populator"]' "$C"
buildah commit -q "$C" "$1"
buildah rm "$C"
