#!/bin/bash
set -euo pipefail
version=${1:?version}
arch=${2:?amd64 or arm64}
case "$arch" in
  amd64) machine=x86_64 ;;
  arm64) machine=aarch64 ;;
  *) echo "unknown arch $arch"; exit 1 ;;
esac
id=io.github.grangedevgroup_code.webcloner
out=packages
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for fmt in deb rpm archlinux; do
  NFPM_ARCH=$arch VERSION=$version nfpm package -f packaging/nfpm.yaml -p "$fmt" -t "$out/"
done

tree="$work/webcloner"
install -Dm755 dist/webcloner "$tree/webcloner"
install -Dm644 packaging/webcloner.desktop "$tree/$id.desktop"
install -Dm644 Icon.png "$tree/$id.png"
install -Dm644 LICENSE "$tree/LICENSE"
install -Dm644 README.md "$tree/README.md"
tar -C "$work" -cJf "$out/WebCloner-linux-$arch.tar.xz" webcloner

appdir="$work/WebCloner.AppDir"
install -Dm755 dist/webcloner "$appdir/usr/bin/webcloner"
install -Dm644 packaging/webcloner.desktop "$appdir/$id.desktop"
install -Dm644 packaging/webcloner.desktop "$appdir/usr/share/applications/$id.desktop"
install -Dm644 Icon.png "$appdir/$id.png"
install -Dm644 Icon.png "$appdir/usr/share/icons/hicolor/512x512/apps/$id.png"
ln -s usr/bin/webcloner "$appdir/AppRun"
tool="$work/appimagetool"
curl -fsSL -o "$tool" "https://github.com/AppImage/appimagetool/releases/download/1.9.1/appimagetool-$machine.AppImage"
chmod +x "$tool"
ARCH=$machine APPIMAGE_EXTRACT_AND_RUN=1 "$tool" --no-appstream "$appdir" "$out/WebCloner-linux-$arch.AppImage"
ls -la "$out"
