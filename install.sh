#!/bin/sh
set -eu

repo="grangedevgroup-code/WebCloner"
base="https://github.com/$repo/releases/latest/download"
id="io.github.grangedevgroup_code.webcloner"

say() { printf '%s\n' "$*"; }
die() { printf 'WebCloner install: %s\n' "$*" >&2; exit 1; }

action=install
for arg in "$@"; do
  case "$arg" in
    --uninstall) action=uninstall ;;
    -h|--help)
      say "Usage: install.sh [--uninstall]"
      say "Installs the latest WebCloner for the current user (Linux and macOS)."
      exit 0 ;;
    *) die "unknown option $arg" ;;
  esac
done

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "curl or wget is needed"
  fi
}

verify() {
  file=$1
  name=$(basename "$file")
  line=$(grep " $name\$" "$tmp/SHA256SUMS" || true)
  [ -n "$line" ] || die "$name is not listed in SHA256SUMS"
  want=${line%% *}
  if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$file" | cut -d' ' -f1)
  else
    got=$(shasum -a 256 "$file" | cut -d' ' -f1)
  fi
  [ "$want" = "$got" ] || die "checksum mismatch for $name"
}

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t webcloner)
trap 'rm -rf "$tmp"' EXIT INT TERM

install_linux() {
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "no Linux build for $(uname -m) yet" ;;
  esac
  data=${XDG_DATA_HOME:-$HOME/.local/share}
  bin=$HOME/.local/bin
  if [ "$action" = uninstall ]; then
    rm -f "$bin/webcloner" "$data/applications/$id.desktop" "$data/icons/hicolor/512x512/apps/$id.png"
    command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$data/applications" >/dev/null 2>&1 || true
    say "WebCloner removed."
    return
  fi
  asset="WebCloner-linux-$arch.tar.xz"
  say "Downloading $asset"
  fetch "$base/$asset" "$tmp/$asset"
  fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS"
  verify "$tmp/$asset"
  tar -xJf "$tmp/$asset" -C "$tmp" || die "could not unpack $asset (is xz installed?)"
  mkdir -p "$bin" "$data/applications" "$data/icons/hicolor/512x512/apps"
  cp "$tmp/webcloner/webcloner" "$bin/webcloner.new"
  chmod 755 "$bin/webcloner.new"
  mv -f "$bin/webcloner.new" "$bin/webcloner"
  cp "$tmp/webcloner/$id.png" "$data/icons/hicolor/512x512/apps/$id.png"
  sed "s#^Exec=webcloner#Exec=$bin/webcloner#" "$tmp/webcloner/$id.desktop" > "$data/applications/$id.desktop"
  command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$data/applications" >/dev/null 2>&1 || true
  command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t "$data/icons/hicolor" >/dev/null 2>&1 || true
  if ! "$bin/webcloner" version >/dev/null 2>&1; then
    say ""
    say "WebCloner is installed but could not start. It needs the system's OpenGL, X11 and Wayland libraries."
    say "Install them with your package manager, for example:"
    say "  Debian, Ubuntu, Mint:  sudo apt install libgl1 libxcursor1 libxi6 libxinerama1 libxrandr2 libxxf86vm1 libwayland-egl1 libxkbcommon0"
    say "  Fedora, RHEL:          sudo dnf install mesa-libGL libXcursor libXi libXinerama libXrandr libXxf86vm libwayland-egl libxkbcommon"
    say "  Arch, Manjaro:         sudo pacman -S libglvnd libxcursor libxi libxinerama libxrandr libxxf86vm wayland libxkbcommon"
    say "  openSUSE:              sudo zypper install libGL1 libXcursor1 libXi6 libXinerama1 libXrandr2 libXxf86vm1 libwayland-egl1 libxkbcommon0"
    say "or install the .deb, .rpm or pacman package from https://github.com/$repo/releases/latest, which pulls them in."
    exit 1
  fi
  say "$("$bin/webcloner" version) installed. Open it from your app menu, or run: $bin/webcloner"
  case ":$PATH:" in
    *":$bin:"*) ;;
    *) say "Add $bin to your PATH to run webcloner from a terminal." ;;
  esac
}

install_macos() {
  if [ "$action" = uninstall ]; then
    rm -rf "/Applications/WebCloner.app" "$HOME/Applications/WebCloner.app"
    say "WebCloner removed."
    return
  fi
  asset="WebCloner-macos.zip"
  say "Downloading $asset"
  fetch "$base/$asset" "$tmp/$asset"
  fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS"
  verify "$tmp/$asset"
  ditto -x -k "$tmp/$asset" "$tmp/app"
  dest=/Applications
  if [ ! -w "$dest" ]; then
    dest=$HOME/Applications
    mkdir -p "$dest"
  fi
  rm -rf "$dest/WebCloner.app"
  ditto "$tmp/app/WebCloner.app" "$dest/WebCloner.app"
  xattr -dr com.apple.quarantine "$dest/WebCloner.app" 2>/dev/null || true
  say "WebCloner installed in $dest. Open it from Launchpad or Applications."
}

case "$(uname -s)" in
  Linux) install_linux ;;
  Darwin) install_macos ;;
  *) die "this script is for Linux and macOS; on Windows use install.ps1" ;;
esac
