#!/bin/sh
# The pre-remove script of the straza package, for deb and rpm alike. On a
# real removal it disables straza-service.socket, and when systemd runs it
# also stops the socket and the service. deb passes "remove" and rpm passes 0
# for that. On an upgrade and every other call it does nothing, because the
# new package restarts the service. A failed systemctl call is reported but
# never blocks the removal.

case "$1" in
remove | 0) ;;
*) exit 0 ;;
esac

# Disabling only removes a symlink, so it also works where systemd is not
# running, which mirrors the post-install script. Stopping needs systemd.
if [ -d /run/systemd/system ]; then
	systemctl --quiet disable --now straza-service.socket || true
	systemctl stop straza-service.service || true
elif command -v systemctl >/dev/null 2>&1; then
	systemctl --quiet disable straza-service.socket || true
fi
exit 0
