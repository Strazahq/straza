# A throwaway machine with systemd as PID 1 for the Linux package rig.
# BASE picks the distribution. run.sh builds one image per base and starts
# it with --privileged, because the service unit's hardening needs
# namespaces. Nothing here installs Straza: the scenarios install the
# package under test with apt-get or dnf.
ARG BASE=ubuntu:24.04
FROM ${BASE}

LABEL org.straza.rig=linux-packages
ENV container=docker

# Debian and Ubuntu bases get systemd and dbus. Their minimal images drop
# files under /usr/share/doc on install, so that filter goes after systemd
# is in, and a package under test installs every file it ships.
# RHEL family bases already run systemd. They get runuser and useradd when
# missing, and lose the nodocs flag for the same reason.
RUN set -eu; \
    if command -v apt-get >/dev/null 2>&1; then \
      export DEBIAN_FRONTEND=noninteractive; \
      apt-get update -qq; \
      apt-get install -y -qq --no-install-recommends systemd systemd-sysv dbus >/dev/null; \
      apt-get clean; \
      rm -rf /var/lib/apt/lists/* /var/log/apt /var/log/dpkg.log; \
      rm -f /etc/dpkg/dpkg.cfg.d/excludes /etc/dpkg/dpkg.cfg.d/docker; \
    else \
      need=""; \
      command -v runuser >/dev/null 2>&1 || need="$need util-linux"; \
      command -v useradd >/dev/null 2>&1 || need="$need shadow-utils"; \
      if [ -n "$need" ]; then dnf install -y -q $need; fi; \
      dnf clean all; \
      rm -rf /var/cache/dnf /var/log/dnf*; \
      sed -i '/^tsflags=nodocs/d' /etc/dnf/dnf.conf; \
    fi; \
    for t in runuser useradd getent systemctl systemd-analyze sha256sum; do \
      command -v "$t" >/dev/null 2>&1 || { echo "missing $t on $(. /etc/os-release; echo "$PRETTY_NAME")"; exit 1; }; \
    done

# Units that touch the host through --privileged, or that fail or wake up
# at random in a container, are masked: sysctl and module loading would
# change the host kernel, a getty would grab a host console, udev would
# manage host devices, and the package timers would race the scenarios.
RUN set -eu; \
    for u in \
      systemd-sysctl.service systemd-modules-load.service kmod-static-nodes.service \
      systemd-binfmt.service proc-sys-fs-binfmt_misc.automount proc-sys-fs-binfmt_misc.mount \
      getty.target console-getty.service getty@tty1.service serial-getty@.service \
      systemd-udevd.service systemd-udevd-control.socket systemd-udevd-kernel.socket \
      systemd-udev-trigger.service systemd-udev-settle.service systemd-hwdb-update.service \
      systemd-remount-fs.service systemd-firstboot.service systemd-random-seed.service \
      systemd-timesyncd.service systemd-resolved.service systemd-journald-audit.socket \
      systemd-pstore.service sys-kernel-debug.mount sys-kernel-tracing.mount \
      sys-kernel-config.mount sys-fs-fuse-connections.mount dev-hugepages.mount \
      apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service \
      dnf-makecache.timer dnf-makecache.service fstrim.timer e2scrub_all.timer \
      e2scrub_reap.service; \
    do ln -sf /dev/null "/etc/systemd/system/$u"; done; \
    : > /etc/machine-id

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
