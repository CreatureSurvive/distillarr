#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Container entrypoint. Started as root (the default), it drops to
# PUID:PGID, adds the groups that own the GPU device nodes passed in
# (/dev/dri, /dev/nvidia*) so hardware encoding works without knowing the
# host's render GID, and makes sure /config belongs to that user.
# Started with `user:` set in compose, it just runs the app as that user
# (add the render group with group_add then).
set -e

umask "${UMASK:-022}"

if [ "$(id -u)" != "0" ]; then
  exec "$@"
fi

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"

groups=""
for dev in /dev/dri/renderD* /dev/dri/card* /dev/nvidia*; do
  [ -e "$dev" ] || continue
  gid="$(stat -c %g "$dev")"
  [ "$gid" = "0" ] && continue
  case ",$groups," in
    *",$gid,"*) ;;
    *) groups="${groups:+$groups,}$gid" ;;
  esac
done

mkdir -p /config
if [ "$(stat -c %u /config)" != "$PUID" ] || [ "$(stat -c %g /config)" != "$PGID" ]; then
  echo "entrypoint: giving /config to $PUID:$PGID"
  chown -R "$PUID:$PGID" /config
fi

if [ -n "$groups" ]; then
  exec setpriv --reuid="$PUID" --regid="$PGID" --groups="$groups" -- "$@"
fi
exec setpriv --reuid="$PUID" --regid="$PGID" --clear-groups -- "$@"
