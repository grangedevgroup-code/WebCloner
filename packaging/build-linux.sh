#!/bin/bash
set -euo pipefail
dnf -y -q install dnf-plugins-core
dnf config-manager --set-enabled powertools
dnf -y -q install gcc tar gzip mesa-libGL-devel libX11-devel libXcursor-devel libXrandr-devel \
  libXinerama-devel libXi-devel libXxf86vm-devel wayland-devel libxkbcommon-devel
version=$(awk '/^go /{print $2}' go.mod)
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64) arch=arm64 ;;
  *) echo "unsupported machine $(uname -m)"; exit 1 ;;
esac
curl -fsSL "https://go.dev/dl/go${version}.linux-${arch}.tar.gz" | tar -C /usr/local -xz
export PATH=/usr/local/go/bin:$PATH GOFLAGS=-buildvcs=false
mkdir -p dist
go build -trimpath -ldflags "-s -w" -o dist/webcloner .
chown -R "$(stat -c %u:%g go.mod)" dist
