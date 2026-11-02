#!/bin/sh
# The pre-install script of the straza package, for deb and rpm alike. It
# creates the straza-service system account before the package's files land,
# because the socket unit names that account. The sysusers file is not on disk
# yet, so its line goes to systemd-sysusers on stdin. Without systemd-sysusers,
# useradd makes the same account. An account that exists is left as it is.
set -e

if command -v systemd-sysusers >/dev/null 2>&1; then
	echo 'u straza-service - "Straza background service" /var/lib/straza-service' |
		SYSTEMD_LOG_LEVEL=warning systemd-sysusers --replace=/usr/lib/sysusers.d/straza-service.conf -
elif id straza-service >/dev/null 2>&1; then
	:
elif command -v useradd >/dev/null 2>&1; then
	shell=/usr/sbin/nologin
	[ -x "$shell" ] || shell=/sbin/nologin
	group=--user-group
	if getent group straza-service >/dev/null 2>&1; then
		group=--gid=straza-service
	fi
	useradd --system "$group" --home-dir /var/lib/straza-service --no-create-home \
		--shell "$shell" --comment "Straza background service" straza-service
else
	echo "straza: the package could not create the straza-service account, because this system has neither systemd-sysusers nor useradd. Install systemd or the shadow utilities, then install the straza package again." >&2
	exit 1
fi

# The service holds every local user's keys, so it must run as a system
# account nobody logs in as. An account made by hand under this name could be
# a person's, so the install stops here, before any file lands.
entry=$(getent passwd straza-service || true)
if [ -z "$entry" ]; then
	echo "straza: the package could not find the straza-service account after creating it, so the Straza background service would have no account to run as. Check that this system's account database accepts local system accounts, then install the straza package again." >&2
	exit 1
fi
uid=$(echo "$entry" | cut -d: -f3)
shell=$(echo "$entry" | cut -d: -f7)
max=$(awk '$1 == "SYS_UID_MAX" { print $2 }' /etc/login.defs 2>/dev/null || true)
case "$max" in '' | *[!0-9]*) max=999 ;; esac
case "$shell" in */nologin | /bin/false | /usr/bin/false) safe=yes ;; *) safe=no ;; esac
case "$uid" in '' | *[!0-9]*) safe=no ;; esac
if [ "$safe" = no ] || [ "$uid" -le 0 ] || [ "$uid" -gt "$max" ]; then
	echo "straza: the account straza-service already exists as uid $uid with the shell $shell, so it is not a system account the Straza background service can safely run as. Remove or rename that account, then install the straza package again." >&2
	exit 1
fi

# deb passes "install" on a fresh install and also on an install after a
# removal that kept the package's record, where post-install still gets an
# old version. The marker tells post-install that both are installs, not
# upgrades. It lives in /run, so a stale marker never survives a reboot.
if [ "$1" = install ] && [ -d /run/systemd/system ]; then
	touch /run/straza-package-install
fi
