#!/bin/sh
# The post-remove script of the straza package, for deb and rpm alike. On a
# removal it deletes the socket's directory and reloads systemd. deb passes
# "remove" for that and keeps /var/lib/straza-service, which holds the keys.
# deb "purge" and an rpm erase, where rpm passes 0, also delete
# /var/lib/straza-service. The straza-service account always stays. An
# upgrade does nothing, and rpm runs this script of the old package with 1
# after the new package's post-install script. A failed call is reported but
# never blocks the removal.

case "$1" in
remove) purge=no ;;
purge | 0) purge=yes ;;
*) exit 0 ;;
esac

rm -rf /run/straza-service
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
fi
if [ "$purge" = yes ]; then
	rm -rf /var/lib/straza-service
fi
exit 0
