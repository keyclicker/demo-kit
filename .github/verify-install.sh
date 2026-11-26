#!/bin/sh
# verify-install.sh: after install.sh, is every tool demo-kit uses there?
# CI runs it in a fresh container per distro family.
set -u
export PATH=$HOME/.local/bin:$PATH
ok=0
check() { if "$@" >/dev/null 2>&1; then echo "  ok   $*"; else echo "  FAIL $*"; ok=1; fi; }
check desk config desk.size
check demo config tts.voice
check deskview config deskview.addr
check sh -c 'command -v Xtigervnc || command -v Xvnc'
check command -v xfwm4
check command -v xauth
check command -v xsetroot
check command -v xdotool
check command -v scrot
check sh -c 'command -v magick || command -v convert'
check command -v pw-record
check command -v pw-play
check sh -c 'ffmpeg -hide_banner -encoders | grep -q libx264'
check sh -c 'ffmpeg -hide_banner -filters | grep -q subtitles'
check sh -c 'fc-list | grep -qi "DejaVu Sans"'
check sh -c 'command -v chromium || command -v chromium-browser || flatpak info --user org.chromium.Chromium'
echo "  browser: $(desk config browser.command)"

# Informational: TigerVNC before 1.14 has no -rendernode, so software GL
if sh -c 'Xtigervnc -help 2>&1 || Xvnc -help 2>&1' | grep -q rendernode; then
  echo "  info Xvnc takes -rendernode: GPU GL"
else
  echo "  info Xvnc has no -rendernode: software GL"
fi
exit $ok
