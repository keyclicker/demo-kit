#!/bin/sh
# install.sh: install demo-kit from this clone, for the current user.
#
#   ./install.sh [--yes] [--no-deps] [--only desk,demo,deskview]
#
#   --yes       answer yes to every question (packages, repos, unit,
#               skills); for scripts and CI
#   --no-deps   skip system packages: just build and set up
#   --only      build only these binaries (default: all three)
#
# Steps: system packages (sudo; Debian/Ubuntu, Fedora/RHEL, Arch and
# their derivatives), a Go check, `go build` into ~/.local/bin, a default
# config, the deskview user unit, agent skills. Safe to rerun.
set -eu

# ===== Arguments =====

yes=0
deps=1
only="desk,demo,deskview"

while [ $# -gt 0 ]; do
  case $1 in
    --yes) yes=1 ;;
    --no-deps) deps=0 ;;
    --only) only=$2; shift ;;
    --only=*) only=${1#--only=} ;;
    -h|--help) sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "install.sh: unknown option $1 (see --help)" >&2; exit 2 ;;
  esac
  shift
done

repo=$(cd "$(dirname "$0")" && pwd)
bin=$HOME/.local/bin
config_dir=${XDG_CONFIG_HOME:-$HOME/.config}/demo-kit

# ===== Helpers =====

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# ask QUESTION: yes with --yes, else ask on the terminal (default yes)
ask() {
  [ "$yes" = 1 ] && return 0
  printf '%s [Y/n] ' "$1"
  read -r answer </dev/tty || return 1
  case $answer in [nN]*) return 1 ;; esac
}

# root CMD...: run as root, through sudo unless already root
root() {
  if [ "$(id -u)" = 0 ]; then "$@"; else sudo "$@"; fi
}

wants() { case ",$only," in *",$1,"*) return 0 ;; esac; return 1; }

# ===== Distro =====

[ "$(uname -s)" = Linux ] || die "demo-kit runs on Linux only"
[ -r /etc/os-release ] || die "no /etc/os-release: unknown distro"

# shellcheck disable=SC1091
. /etc/os-release
family=
for id in ${ID:-} ${ID_LIKE:-}; do
  case $id in
    ubuntu) family=ubuntu; break ;;
    debian) family=debian; break ;;
    fedora) family=fedora; break ;;
    rhel|centos|rocky|almalinux) family=rhel; break ;;
    arch) family=arch; break ;;
  esac
done
# Ubuntu derivatives (Mint, Pop) list "ubuntu debian": Ubuntu wins above

# Chromium: native everywhere except Ubuntu, which only has the snap
browser='"chromium"'
main_profile=
case $family in
  fedora|rhel) browser='"chromium-browser"' ;;
  ubuntu)
    browser='"flatpak", "run", "org.chromium.Chromium"'
    # A literal ~: the config expands it
    # shellcheck disable=SC2088
    main_profile="~/.var/app/org.chromium.Chromium/config/chromium"
    ;;
esac

# ===== System packages =====

# Package names per family: X server, window manager, X tools, recording,
# input and screenshots for agents, caption font, git, Go, sound (pw-play,
# pw-record), a terminal, the browser.
packages() {
  case $1 in
    debian|ubuntu)
      echo tigervnc-standalone-server xauth x11-xserver-utils xfwm4 \
        ffmpeg xdotool scrot imagemagick fonts-dejavu-core git golang-go \
        pipewire-bin xterm
      [ "$1" = debian ] && echo chromium || echo flatpak
      ;;
    fedora|rhel)
      echo tigervnc-server-minimal xorg-x11-xauth xsetroot xfwm4 ffmpeg \
        xdotool scrot ImageMagick dejavu-sans-fonts git golang \
        pipewire-utils xterm chromium
      ;;
    arch)
      echo tigervnc xorg-xauth xorg-xsetroot xfwm4 ffmpeg xdotool scrot \
        imagemagick ttf-dejavu git go pipewire-audio xterm chromium
      ;;
  esac
}

# rpmfusion: Fedora and RHEL's ffmpeg with x264 lives in RPM Fusion
# (Fedora's own ffmpeg-free has no x264); RHEL needs EPEL too.
rpmfusion() {
  rpm -q rpmfusion-free-release >/dev/null 2>&1 && return 0

  ask "Enable RPM Fusion free (ffmpeg with x264)${1:+ and EPEL}?" ||
    die "ffmpeg needs RPM Fusion; rerun, or install it and use --no-deps"

  if [ "$1" = rhel ]; then
    root dnf install -y epel-release
    release=$(rpm -E %rhel)
  else
    release=$(rpm -E %fedora)
  fi

  flavor=fedora
  [ "$1" = rhel ] && flavor=el
  root dnf install -y "https://mirrors.rpmfusion.org/free/$flavor/rpmfusion-free-release-$release.noarch.rpm"
}

install_deps() {
  [ -n "$family" ] ||
    die "unsupported distro ($ID): install the packages by hand (see README), then --no-deps"

  list=$(packages "$family" | tr '\n' ' ')
  say "System packages ($family): $list"
  question="Install them with sudo?"
  [ "$family" = arch ] && question="Upgrade the system and install them (pacman -Syu)?"
  ask "$question" || { warn "skipped packages"; return 0; }

  # Packages are unquoted on purpose: one word each
  # shellcheck disable=SC2086
  case $family in
    debian|ubuntu)
      root apt-get update
      root env DEBIAN_FRONTEND=noninteractive apt-get install -y $list
      ;;
    fedora|rhel)
      rpmfusion "$family"
      # Fedora ships ffmpeg-free; swap it for the full one
      if rpm -q ffmpeg-free >/dev/null 2>&1; then
        root dnf swap -y ffmpeg-free ffmpeg --allowerasing
      fi
      root dnf install -y $list
      ;;
    arch)
      # -u too: Arch doesn't support partial upgrades (-Sy alone)
      root pacman -Syu --needed --noconfirm $list
      ;;
  esac

  if [ "$family" = ubuntu ]; then
    say "Chromium from Flathub (Ubuntu's own is a snap)"
    flatpak remote-add --user --if-not-exists flathub \
      https://dl.flathub.org/repo/flathub.flatpakrepo
    flatpak install --user -y --noninteractive flathub org.chromium.Chromium
  fi
}

[ "$deps" = 1 ] && install_deps

# ===== Go =====

command -v go >/dev/null 2>&1 ||
  die "no go: install Go 1.21+ (https://go.dev/dl/), then rerun"

# Go 1.21+ fetches the toolchain go.mod asks for by itself
go_minor=$(go env GOVERSION | sed -n 's/^go1\.\([0-9]*\).*/\1/p')
[ "${go_minor:-0}" -ge 21 ] ||
  die "$(go env GOVERSION) is too old: install Go 1.21+ (https://go.dev/dl/), then rerun"

# ===== Build =====

mkdir -p "$bin"
for b in desk demo deskview; do
  wants "$b" || continue
  say "Building $b"
  (cd "$repo" && GOTOOLCHAIN=auto go build -trimpath -o "$bin/$b" "./cmd/$b")
done

case :$PATH: in
  *":$bin:"*) ;;
  *) warn "$bin is not on PATH: add it in your shell's profile" ;;
esac

# ===== Config =====

# Only what differs from the built-in defaults; `desk config` shows all
if [ ! -e "$config_dir/config.toml" ]; then
  say "Writing $config_dir/config.toml"
  mkdir -p "$config_dir"
  {
    echo "# demo-kit config: every key is optional. All keys and their"
    echo "# defaults: desk config"
    echo
    echo "[browser]"
    echo "command = [$browser]"
    [ -n "$main_profile" ] && echo "main_profile = \"$main_profile\""
  } >"$config_dir/config.toml"
else
  say "Keeping $config_dir/config.toml"
fi

# ===== deskview unit =====

if wants deskview && command -v systemctl >/dev/null 2>&1 &&
  systemctl --user show-environment >/dev/null 2>&1; then
  units=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user
  mkdir -p "$units"
  cp "$repo/systemd/deskview.service" "$units/"
  systemctl --user daemon-reload

  if ask "Start deskview now and at login (systemctl --user enable --now)?"; then
    systemctl --user enable --now deskview.service
    systemctl --user restart deskview.service
  fi
fi

# ===== Skills =====

# Link each skill into every agent's skills dir that exists
for agent in "$HOME/.claude" "$HOME/.codex"; do
  [ -d "$agent" ] || continue
  ask "Link demo-kit's skills into $agent/skills?" || continue

  if [ -L "$agent/skills" ]; then
    warn "$agent/skills is a symlink (a dotfiles manager?): link by hand"
    continue
  fi

  mkdir -p "$agent/skills"
  for skill in "$repo"/skills/*/; do
    skill=${skill%/}
    ln -sfn "$skill" "$agent/skills/${skill##*/}"
  done
done

# ===== Done =====

say "Done"
wants desk && echo "  desk new      a display for an agent (desk -h)"
if wants demo; then
  key_env=$("$bin/demo" config tts.key_env)
  key=${key_env:+"$key_env in $config_dir/.env"}
  echo "  demo -h       narrated videos; speech key: ${key:-none}"
fi
wants deskview &&
  echo "  deskview      http://$("$bin/deskview" config deskview.addr)/"
exit 0
