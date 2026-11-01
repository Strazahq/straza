#!/bin/sh
# The post-install script of the straza package, for deb and rpm alike. On a
# fresh install it enables straza-service.socket, and when systemd runs it
# also reloads systemd and starts the socket. On an upgrade it restarts a
# running service so it runs the new binary, and leaves the socket as it is,
# even when an admin disabled it.
set -e

# deb passes "configure" with the old version in $2. The pre-install script's
# marker says the run was an install, which covers an install after a removal
# that still passes an old version. rpm passes the count of installed
# versions, 1 on a fresh install.
marker=/run/straza-package-install
case "$1" in
configure) if [ -z "$2" ] || [ -e "$marker" ]; then fresh=yes; else fresh=no; fi ;;
1) fresh=yes ;;
[0-9]*) fresh=no ;;
*) exit 0 ;;
esac
rm -f "$marker"

# Enabling only writes a symlink, so it also works in a chroot or an image
# build where systemd is not running. Reloading and starting need systemd.
if [ "$fresh" = yes ] && command -v systemctl >/dev/null 2>&1; then
	systemctl --quiet enable straza-service.socket
fi
[ -d /run/systemd/system ] || exit 0

systemctl daemon-reload
if [ "$fresh" = yes ]; then
	systemctl start straza-service.socket
else
	systemctl try-restart straza-service.service
fi
