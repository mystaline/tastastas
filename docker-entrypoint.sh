#!/bin/sh
# Preload the baked Go module cache into GOMODCACHE. /tmp is tmpfs in prod
# (image content at /go-mod-cache is read-only), so copy at container start.
if [ -d /go-mod-cache ] && [ -n "$(ls -A /go-mod-cache 2>/dev/null)" ]; then
  mkdir -p /tmp/go/pkg/mod
  cp -r /go-mod-cache/. /tmp/go/pkg/mod/
fi
exec ./tastastas "$@"
