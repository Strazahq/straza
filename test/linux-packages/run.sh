#!/usr/bin/env bash
# run.sh drives the Linux package rig. Each distribution boots in Docker
# with systemd as PID 1, installs the straza deb or rpm package, and checks
# the service, the hooks of three people and an agent, an upgrade while
# windows are open, a managed install, a browser sign-in, a cloned machine
# and removal.
# Every check prints one PASS or FAIL line with its evidence.
# Nothing here installs or runs straza on the host.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
label=org.straza.rig=linux-packages
prefix=straza-pkg
port=8420
server_url=http://strazad:$port
hook_cmd="/usr/bin/straza hook --harness claude-code"
disk_limit=${RIG_DISK_LIMIT:-90}
all_distros=(ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 debian-12 debian-13 ubi-9 ubi-10 rocky-9 rocky-10)
windows=(01 02 03 04 05 06 07 08 09 10 11 12 13 14 15)

usage() {
	cat <<'EOF'
Usage:
  run.sh boot [--keep] [DISTRO...]        build each image and boot it once
  run.sh test [--keep] PKGDIR [DISTRO...] run every scenario on each distribution
  run.sh clean [--bases]                  remove the rig's containers, networks and images

DISTRO is one of ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 debian-12 debian-13
ubi-9 ubi-10 rocky-9 rocky-10, or all, which is the default.

boot and test remove each distribution's image when they finish with it,
and prune Docker's unused build cache, which holds the pulled base, because
the rig runs on a small disk. --keep leaves the image and, for test, the
containers in place for inspection, and clean removes them later. Before
each build the run stops when the disk holding Docker's data is at or over
RIG_DISK_LIMIT percent full, 90 by default. clean --bases also removes the
base images the rig pulls and prunes the build cache.

PKGDIR holds two builds of the straza package from one tree, an older and a
newer version, each as an amd64 deb and an x86_64 rpm, plus the linux amd64
strazad binary of the newer build. The older one is installed first and the
newer one is the upgrade. From the repository root:

  pkgs=$(mktemp -d)
  GORELEASER_CURRENT_TAG=v1.0.1 goreleaser release --snapshot --clean --skip=sign,sbom
  cp dist/*.deb dist/*.rpm "$pkgs"/
  GORELEASER_CURRENT_TAG=v1.0.2 goreleaser release --snapshot --clean --skip=sign,sbom
  cp dist/*.deb dist/*.rpm dist/strazad_linux_amd64_v1/strazad "$pkgs"/
  rm -rf dist
  test/linux-packages/run.sh test "$pkgs" all

Any two tags work when the second is higher. The run needs docker, curl and
jq on the host. Logs, hook outputs and journals go to RIG_WORK, a new
temporary directory by default, whose path the run prints first.
EOF
}

base_of() {
	case $1 in
	ubuntu-22.04) echo ubuntu:22.04 ;;
	ubuntu-24.04) echo ubuntu:24.04 ;;
	ubuntu-26.04) echo ubuntu:26.04 ;;
	debian-12) echo debian:12-slim ;;
	debian-13) echo debian:13-slim ;;
	ubi-9) echo registry.access.redhat.com/ubi9/ubi-init:latest ;;
	ubi-10) echo registry.access.redhat.com/ubi10/ubi-init:latest ;;
	rocky-9) echo rockylinux/rockylinux:9-ubi-init ;;
	rocky-10) echo rockylinux/rockylinux:10-ubi-init ;;
	*) return 1 ;;
	esac
}

family_of() {
	case $1 in
	ubuntu-* | debian-*) echo deb ;;
	*) echo rpm ;;
	esac
}

say() { printf '.. %s\n' "$*" >&2; }

die() {
	printf 'run.sh: %s\n' "$*" >&2
	exit 2
}

npass=0
nfail=0
distro=-
ctr='' sd='' net='' clone='' dw='' hd='' fam='' api=''

flat() { tr '\n\r\t' '   ' | tr -s ' ' | cut -c1-600; }

# result prints one check line and counts it. OK is 1 for a pass.
result() {
	local ok=$1 id=$2 text
	shift 2
	text=$(printf '%s' "$*" | flat)
	if [ "$ok" = 1 ]; then
		npass=$((npass + 1))
		printf 'PASS %-12s %-18s %s\n' "$distro" "$id" "$text"
	else
		nfail=$((nfail + 1))
		printf 'FAIL %-12s %-18s %s\n' "$distro" "$id" "$text"
	fi
}

# info prints an observation that is not judged.
info() {
	local id=$1
	shift
	printf 'INFO %-12s %-18s %s\n' "$distro" "$id" "$(printf '%s' "$*" | flat)"
}

is() { if "$@"; then echo 1; else echo 0; fi; }

redact() {
	sed -E 's/("(password|token|id_token|access_token|refresh_token|device_code)"[[:space:]]*:[[:space:]]*")[^"]*"/\1<redacted>"/g'
}

disk_pct() {
	local root
	root=$(docker info -f '{{.DockerRootDir}}' 2>/dev/null) || root=/
	df -P "$root" | awk 'NR==2 { gsub("%", "", $5); print $5 }'
}

disk_guard() {
	local pct
	pct=$(disk_pct)
	if [ "$pct" -ge "$disk_limit" ]; then
		printf "run.sh: the disk holding Docker's data is %s%% full, at or over the %s%% limit, so the run stops before it adds an image. Free space, or run 'run.sh clean', then run this again.\n" "$pct" "$disk_limit" >&2
		return 1
	fi
}

make_work() {
	work=${RIG_WORK:-$(mktemp -d "${TMPDIR:-/tmp}/straza-pkg-rig.XXXXXX")}
	mkdir -p "$work"
	say "work directory $work"
}

pick_distros() {
	sel=()
	if [ $# -eq 0 ] || [ "$1" = all ]; then
		sel=("${all_distros[@]}")
		return
	fi
	local d
	for d in "$@"; do
		base_of "$d" >/dev/null || die "unknown distribution $d. Pick from ${all_distros[*]}, or all."
		sel+=("$d")
	done
}

build_image() {
	say "building $prefix-sd:$1 from $(base_of "$1")"
	docker build --build-arg "BASE=$(base_of "$1")" -t "$prefix-sd:$1" -f "$here/Dockerfile" "$here" \
		>"$work/build-$1.log" 2>&1
}

image_size() { docker image ls --format '{{.Size}}' "$1" | head -1; }

new_machine_id() { tr -d '-' </proc/sys/kernel/random/uuid; }

# start_machine creates a systemd container on a network, gives it the
# machine id it is told, boots it and prints the system state.
start_machine() {
	local name=$1 image=$2 mid=$3 net=$4
	docker create --name "$name" --hostname "$name" --label "$label" --privileged \
		--cgroupns private --tmpfs /run:mode=0755 --tmpfs /run/lock --tmpfs /tmp:mode=1777 \
		--network "$net" "$image" >/dev/null || return 1
	printf '%s\n' "$mid" >"$work/$name.machine-id"
	chmod 0444 "$work/$name.machine-id"
	docker cp -q "$work/$name.machine-id" "$name:/etc/machine-id" || return 1
	rm -f "$work/$name.machine-id"
	docker start "$name" >/dev/null || return 1
	# systemctl fails at once, without waiting, until PID 1 opens its socket.
	for _ in $(seq 1 150); do
		docker exec "$name" test -S /run/systemd/private </dev/null 2>/dev/null && break
		sleep 0.2
	done
	timeout 120 docker exec "$name" systemctl is-system-running --wait </dev/null 2>&1 | tail -1
}

booted() { [ "$1" = running ] || [ "$1" = degraded ]; }

drop_images() {
	docker image rm "$@" >/dev/null 2>&1
	docker builder prune -f >/dev/null 2>&1
}

remove_containers() {
	local c
	for c in "$@"; do docker rm -f "$c" >/dev/null 2>&1; done
}

cmd_boot() {
	local keep=0 d ctr net probe state failed dns
	if [ "${1:-}" = --keep ]; then
		keep=1
		shift
	fi
	pick_distros "$@"
	make_work
	for d in "${sel[@]}"; do
		distro=$d
		disk_guard || break
		say "disk before $d: $(disk_pct)%"
		if ! build_image "$d"; then
			result 0 boot.build "image build failed: $(tail -5 "$work/build-$d.log")"
			continue
		fi
		ctr=$prefix-boot-$d net=$prefix-net-$d probe=$prefix-probe-$d
		docker network create --label "$label" "$net" >/dev/null
		docker run -d --name "$probe" --label "$label" --network "$net" --network-alias strazad \
			alpine:3 sleep 600 >/dev/null
		state=$(start_machine "$ctr" "$prefix-sd:$d" "$(new_machine_id)" "$net")
		failed=$(docker exec "$ctr" systemctl --failed --no-legend --plain </dev/null 2>&1 | awk '{print $1}')
		dns=$(docker exec "$ctr" getent hosts strazad </dev/null 2>&1)
		result "$(is booted "$state")" boot.systemd "state ${state:-none}, failed units: ${failed:-none}," \
			"$(docker exec "$ctr" systemctl --version </dev/null 2>&1 | head -1)"
		result "$(is test -n "$dns")" boot.dns "strazad resolves to ${dns:-nothing}"
		info boot.image "$prefix-sd:$d is $(image_size "$prefix-sd:$d"), disk $(disk_pct)%"
		remove_containers "$ctr" "$probe"
		docker network rm "$net" >/dev/null 2>&1
		if [ "$keep" = 0 ]; then drop_images "$prefix-sd:$d"; fi
	done
}

cmd_clean() {
	local d
	docker ps -aq --filter "label=$label" | xargs -r docker rm -f >/dev/null
	docker network ls -q --filter "label=$label" | xargs -r docker network rm >/dev/null
	docker image ls -q --filter "reference=$prefix-sd" --filter "reference=$prefix-clone" | sort -u |
		xargs -r docker image rm -f >/dev/null
	if [ "${1:-}" = --bases ]; then
		for d in "${all_distros[@]}"; do docker image rm "$(base_of "$d")" >/dev/null 2>&1; done
		docker builder prune -f >/dev/null
	fi
	say "clean: done, disk $(disk_pct)%"
}

# The scenarios below share these globals for the distribution in play:
# ctr, net, sd and clone name its containers, dw and hd its work and hook
# directories, fam its package format and api the host URL of its strazad.
cx() { docker exec "$ctr" "$@" </dev/null; }

absent() { ! cx test -e "$1"; }

ss_payload() {
	jq -nc --arg s "$1" --arg c "$2" '{hook_event_name: "SessionStart", session_id: $s, cwd: $c, source: "startup"}'
}

pre_payload() {
	jq -nc --arg s "$1" --arg c "$2" --arg cmd "$3" \
		'{hook_event_name: "PreToolUse", session_id: $s, cwd: $c, tool_name: "Bash", tool_input: {command: $cmd}}'
}

bg_pids=()

# hook_bg runs one hook as a local user in the background. KEY names its
# output files under hd, and COMMAND defaults to the packaged hook.
hook_bg() {
	local key=$1 user=$2 payload=$3 cmd=${4:-$hook_cmd}
	(
		printf '%s' "$payload" | timeout 90 docker exec -i "$ctr" runuser -u "$user" -- sh -c "$cmd" \
			>"$hd/$key.out" 2>"$hd/$key.err"
		echo "${PIPESTATUS[1]}" >"$hd/$key.rc"
	) &
	bg_pids+=($!)
}

hook_wait() {
	[ ${#bg_pids[@]} -eq 0 ] || wait "${bg_pids[@]}"
	bg_pids=()
}

rc_of() { cat "$hd/$1.rc" 2>/dev/null || echo none; }
hook_field() { jq -r "$2 // empty" "$hd/$1.out" 2>/dev/null | head -1; }
hook_evidence() {
	printf 'exit %s, stdout %s, stderr %s' "$(rc_of "$1")" "$(head -c 300 "$hd/$1.out")" "$(head -c 300 "$hd/$1.err")"
}

is_started() {
	[ "$(rc_of "$1")" = 0 ] && [[ $(hook_field "$1" .systemMessage) == *"Straza governance active"* ]]
}

is_allowed() {
	[ "$(rc_of "$1")" = 0 ] && [ "$(hook_field "$1" .hookSpecificOutput.permissionDecision)" = allow ]
}

is_denied() {
	[ "$(rc_of "$1")" = 2 ] && [ "$(hook_field "$1" .hookSpecificOutput.permissionDecision)" = deny ] &&
		[[ $(hook_field "$1" .hookSpecificOutput.permissionDecisionReason) == *"recursive force-delete"* ]]
}

check_hook() {
	local id=$1 kind=$2 key=$3 text
	case $kind in
	started) text=$(hook_field "$key" .systemMessage) ;;
	allowed) text="exit 0, permissionDecision allow" ;;
	denied) text="exit 2, deny: $(hook_field "$key" .hookSpecificOutput.permissionDecisionReason)" ;;
	esac
	if "is_$kind" "$key"; then result 1 "$id" "$text"; else result 0 "$id" "$(hook_evidence "$key")"; fi
}

# check_refused passes when the hook exits 2 and its stderr holds FRAGMENT.
check_refused() {
	local id=$1 key=$2 fragment=$3
	if [ "$(rc_of "$key")" = 2 ] && grep -qF "$fragment" "$hd/$key.err"; then
		result 1 "$id" "exit 2: $(head -c 300 "$hd/$key.err")"
	else
		result 0 "$id" "wanted exit 2 with \"$fragment\": $(hook_evidence "$key")"
	fi
}

# batch_check judges many hooks of one kind and prints one line.
batch_check() {
	local id=$1 kind=$2 k ok=0 bad=()
	shift 2
	for k in "$@"; do
		if "is_$kind" "$k"; then ok=$((ok + 1)); else bad+=("[$k: $(hook_evidence "$k")]"); fi
	done
	result "$(is test ${#bad[@]} -eq 0)" "$id" "$ok of $# $kind. ${bad[*]:0:3}"
}

approve() {
	local code=$1 user=$2 pwfile=$3 status=000
	for _ in 1 2 3 4 5 6 7 8; do
		status=$(curl -sS -o "$dw/approve.$user.out" -w '%{http_code}' -X POST "$api/oidc/device" \
			--data-urlencode "user_code=$code" --data-urlencode "username=$user" \
			--data-urlencode "password@$pwfile")
		[ "$status" = 429 ] || break
		sleep 2
	done
	approve_status=$status
	case $status in 2?? | 3??) return 0 ;; esac
	return 1
}

# admin_login signs the bootstrap admin in through the device flow under the
# console's client id and keeps the ID token, the person's login token the
# admin API takes, as the bearer header in a private file.
admin_login() {
	local body uc dc tok
	body=$(curl -sS -X POST "$api/oidc/device_authorization" -d client_id=console -d scope=openid)
	uc=$(jq -r '.user_code // empty' <<<"$body")
	dc=$(jq -r '.device_code // empty' <<<"$body")
	if [ -z "$uc" ] || [ -z "$dc" ]; then
		login_error="device_authorization answered $(redact <<<"$body")"
		return 1
	fi
	if ! approve "$uc" admin "$dw/admin.pw"; then
		login_error="the approval as admin got HTTP $approve_status: $(redact <"$dw/approve.admin.out" | head -c 300)"
		return 1
	fi
	for _ in $(seq 1 30); do
		body=$(curl -sS -X POST "$api/oidc/token" -d client_id=console \
			--data-urlencode "grant_type=urn:ietf:params:oauth:grant-type:device_code" --data-urlencode "device_code=$dc")
		tok=$(jq -r '.id_token // empty' <<<"$body")
		if [ -n "$tok" ]; then
			printf 'Authorization: Bearer %s\n' "$tok" >"$dw/admin.hdr"
			return 0
		fi
		case $(jq -r '.error // empty' <<<"$body") in
		authorization_pending | slow_down) sleep 1 ;;
		*)
			login_error="the token poll answered $(redact <<<"$body")"
			return 1
			;;
		esac
	done
	login_error="the device-flow token never arrived"
	return 1
}

# api_call sends one admin API request and sets api_status and api_body.
api_call() {
	local args=(-sS -o "$dw/api.out" -w '%{http_code}' -X "$1" -H "@$dw/admin.hdr")
	if [ $# -ge 3 ]; then
		printf '%s' "$3" >"$dw/api.req"
		args+=(-H 'Content-Type: application/json' --data-binary "@$dw/api.req")
	fi
	api_status=$(curl "${args[@]}" "$api$2")
	api_body=$(cat "$dw/api.out" 2>/dev/null)
}

created() { case $api_status in 2??) return 0 ;; esac; return 1; }

declare -A user_id enroll_pid

create_person() {
	local u=$1
	printf 'Rig-%s-2026!' "$u" >"$dw/$u.pw"
	api_call POST /v1/admin/users "$(jq -nc --arg u "$u" --rawfile p "$dw/$u.pw" \
		'{username: $u, password: $p, email: ($u + "@rig.test"), display: $u}')"
	user_id[$u]=$(jq -r '.id // empty' <<<"$api_body" 2>/dev/null)
	result "$(is created)" a.user-"$u" "HTTP $api_status, id ${user_id[$u]:-none}"
}

create_agent() {
	api_call POST /v1/admin/users '{"username":"build-agent","display":"Build agent","kind":"nhi","user_type":"agent"}'
	user_id[build-agent]=$(jq -r '.id // empty' <<<"$api_body" 2>/dev/null)
	result "$(is created)" a.user-agent "HTTP $api_status, id ${user_id[build-agent]:-none}"
}

# audit_count counts the audit records whose event carries TEXT.
audit_count() {
	local after=0 n total=0
	while :; do
		api_call GET "/v1/admin/audit?after=$after&limit=1000"
		if [ "$api_status" != 200 ]; then
			echo "unknown, the audit API answered HTTP $api_status"
			return
		fi
		n=$(jq length <<<"$api_body")
		total=$((total + $(jq --arg t "$1" '[.[] | select(.ce | contains($t))] | length' <<<"$api_body")))
		[ "$n" -lt 1000 ] && break
		after=$(jq '.[-1].seq' <<<"$api_body")
	done
	echo "$total"
}

pkg_install() {
	if [ "$fam" = deb ]; then
		cx env DEBIAN_FRONTEND=noninteractive apt-get install -y "/opt/rig/$1.deb"
	else
		cx dnf install -y "--disablerepo=*" "/opt/rig/$1.rpm"
	fi
}

pkg_file_version() {
	if [ "$fam" = deb ]; then cx dpkg-deb -f "/opt/rig/$1.deb" Version; else
		cx rpm -qp --qf '%{VERSION}-%{RELEASE}' "/opt/rig/$1.rpm"
	fi
}

installed_version() {
	# shellcheck disable=SC2016 # dpkg-query expands ${Version} itself
	if [ "$fam" = deb ]; then cx dpkg-query -W -f '${Version}' straza; else
		cx rpm -q --qf '%{VERSION}-%{RELEASE}' straza
	fi
}

version_lt() {
	if [ "$fam" = deb ]; then cx dpkg --compare-versions "$1" lt "$2"; else
		[ "$(cx rpm --eval "%{lua: print(rpm.vercmp('$1', '$2'))}")" = -1 ]
	fi
}

# find_pkgs picks the two straza packages of the distribution's format from
# PKGDIR, ordered by version, into old_pkg and new_pkg.
find_pkgs() {
	local -a found
	if [ "$fam" = deb ]; then
		mapfile -t found < <(find "$pkgdir" -maxdepth 1 -type f -name 'straza[_-][0-9]*.deb' -name '*amd64*' | sort -V)
	else
		mapfile -t found < <(find "$pkgdir" -maxdepth 1 -type f -name 'straza[_-][0-9]*.rpm' \
			\( -name '*x86_64*' -o -name '*amd64*' \) | sort -V)
	fi
	[ ${#found[@]} -eq 2 ] || return 1
	old_pkg=${found[0]} new_pkg=${found[1]}
}

start_strazad() {
	docker create --name "$sd" --label "$label" --network "$net" --network-alias strazad \
		-p "127.0.0.1::$port" -e "STRAZA_PUBLIC_URL=$server_url" alpine:3 \
		/usr/local/bin/strazad serve --profile standalone --data-dir /var/lib/strazad --listen "0.0.0.0:$port" \
		>/dev/null && docker cp -q "$strazad_bin" "$sd:/usr/local/bin/strazad" && docker start "$sd" >/dev/null
}

scenario_a() {
	if ! start_strazad; then
		result 0 a.strazad "the strazad container did not start"
		return 1
	fi
	api=http://$(docker port "$sd" "$port/tcp" | head -1)
	local version
	for _ in $(seq 1 120); do
		curl -fsS -o /dev/null "$api/readyz" 2>/dev/null && break
		sleep 0.5
	done
	version=$(curl -fsS "$api/version" 2>/dev/null)
	result "$(is test -n "$version")" a.strazad "ready at $api, /version ${version:-none}"
	[ -n "$version" ] || return 1
	docker logs "$sd" 2>&1 | grep 'bootstrap admin created' | sed -n 's/.*"password":"\([^"]*\)".*/\1/p' |
		head -1 | tr -d '\n' >"$dw/admin.pw"
	if ! [ -s "$dw/admin.pw" ] || ! admin_login; then
		result 0 a.admin "${login_error:-strazad logged no bootstrap admin password}"
		return 1
	fi
	result 1 a.admin "the bootstrap admin signed in through the device flow"
	create_person alice
	create_person bob
	create_person dave
	create_person erin
	create_agent
}

machine_up() {
	local state u
	state=$(start_machine "$ctr" "$prefix-sd:$distro" "$(new_machine_id)" "$net")
	result "$(is booted "$state")" a.machine "state ${state:-none}, $(cx systemctl --version 2>&1 | head -1)"
	booted "$state" || return 1
	mkdir -p "$dw/pkgs"
	cp "$old_pkg" "$dw/pkgs/old.$fam" && cp "$new_pkg" "$dw/pkgs/new.$fam"
	chmod 0755 "$dw/pkgs" && chmod 0644 "$dw/pkgs/old.$fam" "$dw/pkgs/new.$fam"
	docker cp -q "$dw/pkgs" "$ctr:/opt/rig"
	rm -r "$dw/pkgs"
	for u in alice bob carol dave erin build-agent; do
		cx useradd -m "$u" >/dev/null 2>&1 || result 0 a.local-users "useradd -m $u failed in $ctr"
	done
	uid_alice=$(cx id -u alice)
	local old new
	old=$(pkg_file_version old) new=$(pkg_file_version new)
	result "$(is version_lt "$old" "$new")" a.versions "install $(basename "$old_pkg") ($old), upgrade to $(basename "$new_pkg") ($new)"
}

socket_enabled_listening() { grep -qx enabled <<<"$1" && grep -q 'SubState=listening' <<<"$1"; }

scenario_b() {
	local out rc pw sock rundir idle helper verify score
	out=$(pkg_install old 2>&1)
	rc=$?
	printf '%s\nexit %s\n' "$out" "$rc" >"$dw/install-old.txt"
	cx systemctl is-active straza-service.socket >"$dw/socket-after-install.txt" 2>&1
	result "$(is test "$rc" = 0)" b.install "exit $rc, installed $(installed_version 2>&1): $(tail -3 <<<"$out")"
	[ "$rc" = 0 ] || return 1
	pw=$(cx getent passwd straza-service)
	result "$(is test -n "$pw" -a "${pw##*/}" = nologin)" b.account "${pw:-no straza-service account}"
	sock=$(cx systemctl is-enabled straza-service.socket; cx systemctl show -p ActiveState -p SubState -p Listen straza-service.socket)
	result "$(is socket_enabled_listening "$sock")" b.socket-unit "$sock"
	rundir=$(cx stat -c '%a %U:%G %F' /run/straza-service 2>&1)
	result "$(is test "$rundir" = '755 root:root directory')" b.rundir "/run/straza-service is $rundir"
	sock=$(cx stat -c '%a %U %F' /run/straza-service/service.sock 2>&1)
	result "$(is test "$sock" = '666 straza-service socket')" b.socket-file "service.sock is $sock"
	idle=$(cx systemctl is-active straza-service.service)
	result "$(is test "$idle" = inactive)" b.idle "straza-service.service is $idle before the first connection"
	helper=$(cx readlink /usr/bin/git-remote-straza 2>&1)
	result "$(is test "$helper" = /usr/bin/straza)" b.git-helper "/usr/bin/git-remote-straza points to ${helper:-nothing}"
	verify=$(cx systemd-analyze verify /usr/lib/systemd/system/straza-service.socket \
		/usr/lib/systemd/system/straza-service.service 2>&1 | grep straza-service)
	result "$(is test -z "$verify")" b.verify "systemd-analyze verify: ${verify:-no warnings for the two units}"
	cx systemd-analyze security --no-pager straza-service.service >"$dw/security.txt" 2>&1
	score=$(grep 'Overall exposure level' "$dw/security.txt" | sed 's/.*: //')
	result "$(is test -n "$score")" b.security "exposure ${score:-not reported: $(tail -2 "$dw/security.txt")}"
}

# enroll_bg starts `straza enroll` with the given arguments as a local user
# in the background.
enroll_bg() {
	local user=$1
	shift
	timeout 180 docker exec "$ctr" runuser -u "$user" -- /usr/bin/straza enroll "$@" \
		</dev/null >"$dw/enroll.$user.log" 2>&1 &
	enroll_pid[$user]=$!
}

# enroll_person runs enroll_bg and approves the code it prints, so its
# arguments include --use-device-code. It sets enroll_rc to the exit code of
# `straza enroll`.
enroll_person() {
	local user=$1 code
	shift
	enroll_bg "$user" "$@"
	enroll_rc=none
	if code=$(wait_code "$user") && approve "$code" "$user" "$dw/$user.pw"; then
		wait "${enroll_pid[$user]}"
		enroll_rc=$?
	fi
}

wait_code() {
	local code
	for _ in $(seq 1 300); do
		code=$(grep -oE 'confirm code [A-Z2-9]{4}-[A-Z2-9]{4}' "$dw/enroll.$1.log" 2>/dev/null | head -1)
		if [ -n "$code" ]; then
			printf '%s' "${code##* }"
			return 0
		fi
		kill -0 "${enroll_pid[$1]}" 2>/dev/null || return 1
		sleep 0.2
	done
	return 1
}

# scenario_cd enrolls alice and bob at the same time, runs their hooks at
# the same time, and asks for carol, who never enrolled.
scenario_cd() {
	local u s code rc line
	enroll_bg alice --use-device-code --server "$server_url"
	enroll_bg bob --use-device-code --server "$server_url"
	for u in alice bob; do
		s=c
		[ "$u" = bob ] && s=d
		if ! code=$(wait_code "$u"); then
			result 0 "$s.$u-code" "no confirm code: $(tail -c 400 "$dw/enroll.$u.log")"
		elif ! approve "$code" "$u" "$dw/$u.pw"; then
			result 0 "$s.$u-approve" "HTTP $approve_status: $(head -c 300 "$dw/approve.$u.out")"
		fi
	done
	for u in alice bob; do
		s=c
		[ "$u" = bob ] && s=d
		wait "${enroll_pid[$u]}"
		rc=$?
		line=$(grep -oE 'Enrolled as [^ ]+ \(device [^)]+\)' "$dw/enroll.$u.log")
		result "$(is test "$rc" = 0 -a -n "$line")" "$s.$u-enroll" "exit $rc: $(tail -c 300 "$dw/enroll.$u.log")"
	done
	device_alice=$(sed -n 's/.*Enrolled as alice (device \([^)]*\)).*/\1/p' "$dw/enroll.alice.log" | head -1)
	for u in alice bob; do hook_bg "$u-start" "$u" "$(ss_payload "$u-w00" "/home/$u")"; done
	hook_wait
	for u in alice bob; do
		hook_bg "$u-allow" "$u" "$(pre_payload "$u-w00" "/home/$u" "git status")"
		hook_bg "$u-deny" "$u" "$(pre_payload "$u-w00" "/home/$u" "rm -rf /home/$u/project")"
	done
	hook_bg carol-start carol "$(ss_payload carol-w00 /home/carol)"
	hook_wait
	check_hook c.alice-start started alice-start
	check_hook c.alice-allow allowed alice-allow
	check_hook c.alice-deny denied alice-deny
	check_hook d.bob-start started bob-start
	check_hook d.bob-allow allowed bob-allow
	check_hook d.bob-deny denied bob-deny
	check_refused d.carol-refused carol-start "This machine isn't enrolled for you yet."
	line=$(cx stat -c '%a %U %F' /run/straza-service/git /run/straza-service/git/hosts.conf 2>&1 | tr '\n' ',')
	result "$(is test "$line" = '755 straza-service directory,644 straza-service regular empty file,')" c.hostfile \
		"git folder and host file: $line"
}

scenario_e() {
	local out rc key
	out=$(cx runuser -u build-agent -- /usr/bin/straza keygen --user build-agent 2>&1)
	rc=$?
	key=$(sed -n 's/^Public key: //p' <<<"$out" | head -1)
	result "$(is test "$rc" = 0 -a -n "$key")" e.keygen "exit $rc, public key ${key:0:12}..."
	[ -n "$key" ] || return
	api_call PUT "/v1/admin/users/${user_id[build-agent]}/nhi-key" "$(jq -nc --arg k "$key" '{public_key: $k}')"
	result "$(is created)" e.register "HTTP $api_status $(head -c 200 <<<"$api_body")"
	out=$(cx runuser -u build-agent -- /usr/bin/straza enroll --headless --user build-agent --server "$server_url" 2>&1)
	rc=$?
	result "$(is test "$rc" = 0)" e.enroll "exit $rc: $out"
	hook_bg agent-start build-agent "$(ss_payload agent-w00 /home/build-agent)"
	hook_wait
	hook_bg agent-allow build-agent "$(pre_payload agent-w00 /home/build-agent "git status")"
	hook_wait
	check_hook e.agent-start started agent-start
	check_hook e.agent-decide allowed agent-allow
}

scenario_f() {
	local w keys=()
	for w in "${windows[@]}"; do
		hook_bg "w$w-start" alice "$(ss_payload "alice-w$w" /home/alice)"
		keys+=("w$w-start")
	done
	hook_wait
	batch_check f.starts started "${keys[@]}"
	keys=()
	for w in "${windows[@]}"; do
		hook_bg "w$w-allow" alice "$(pre_payload "alice-w$w" /home/alice "git status")"
		keys+=("w$w-allow")
	done
	hook_wait
	batch_check f.decides allowed "${keys[@]}"
}

# scenario_g upgrades the package while alice's 15 windows hold sessions,
# then asks each window again without a new session start.
scenario_g() {
	local before after out rc want got w keys=() reused st enr
	before=$(cx systemctl show -p MainPID --value straza-service.service)
	want=$(pkg_file_version new)
	out=$(pkg_install new 2>&1)
	rc=$?
	printf '%s\nexit %s\n' "$out" "$rc" >"$dw/install-new.txt"
	got=$(installed_version 2>&1)
	result "$(is test "$rc" = 0 -a "$got" = "$want")" g.install "exit $rc, installed $got, package $want: $(tail -3 <<<"$out")"
	after=$(cx systemctl show -p MainPID --value straza-service.service)
	st=$(cx systemctl is-active straza-service.service)
	result "$(is test "$st" = active -a "$after" != "$before" -a "$after" != 0)" g.restart \
		"straza-service.service is $st, main pid $before before and $after after"
	st=$(cx stat -c '%a %U %F' /run/straza-service/git /run/straza-service/git/hosts.conf 2>&1 | tr '\n' ',')
	result "$(is test "$st" = '755 straza-service directory,644 straza-service regular empty file,')" g.hostfile \
		"git folder and host file after the upgrade: $st"
	st=$(cx systemctl show -p ActiveState -p SubState straza-service.socket | tr '\n' ' ')
	st="$st$(cx stat -c ', /run/straza-service %a %U' /run/straza-service 2>&1)"
	result "$(is grep -qE 'ActiveState=active SubState=(listening|running)' <<<"$st")" g.socket "$st"
	for w in "${windows[@]}"; do
		hook_bg "w$w-upgraded" alice "$(pre_payload "alice-w$w" /home/alice "git status")"
		keys+=("w$w-upgraded")
	done
	hook_wait
	batch_check g.windows allowed "${keys[@]}"
	sleep 2 # strazad writes audit records a moment after it answers
	reused=$(audit_count device.key.reused)
	result "$(is test "$reused" = 0)" g.no-reuse "$reused device.key.reused records at strazad"
	api_call GET "/v1/admin/users/${user_id[alice]}/devices"
	st=$(jq -r --arg d "$device_alice" '.[] | select(.id == $d) | .status' <<<"$api_body" 2>/dev/null)
	result "$(is test "$st" = active)" g.device "alice's device ${device_alice:-unknown} is ${st:-not listed}, HTTP $api_status"
	enr=$(cx stat -c '%a %U %s bytes' "/var/lib/straza-service/u/$uid_alice/enrollment.json" 2>&1)
	result "$(is test "${enr%% *}" = 600)" g.enrollment "u/$uid_alice/enrollment.json: $enr"
}

scenario_h() {
	local out rc cmds bad start m
	out=$(cx /usr/bin/straza install --managed --server "$server_url" claude-code 2>&1)
	rc=$?
	printf '%s\nexit %s\n' "$out" "$rc" >"$dw/managed-install.txt"
	result "$(is test "$rc" = 0)" h.install "exit $rc: $(tail -c 400 <<<"$out")"
	cx cat /etc/claude-code/managed-settings.json >"$dw/managed-settings.json" 2>&1
	cmds=$(jq -r '[.hooks[][].hooks[].command] | unique | join(", ")' "$dw/managed-settings.json" 2>/dev/null)
	bad=$(jq '[.hooks[][].hooks[].command | select(startswith("/usr/bin/straza ") | not)] | length' \
		"$dw/managed-settings.json" 2>/dev/null)
	result "$(is test -n "$cmds" -a "$bad" = 0)" h.wiring "hook commands: ${cmds:-none: $(head -c 200 "$dw/managed-settings.json")}"
	result "$(is absent /usr/local/bin/straza)" h.no-copy "$(cx sh -c 'ls -l /usr/local/bin/straza 2>&1')"
	start=$(jq -r '.hooks.SessionStart[0].hooks[0].command // empty' "$dw/managed-settings.json" 2>/dev/null)
	hook_bg managed-start alice "$(ss_payload alice-managed /home/alice)" "${start:-$hook_cmd}"
	hook_wait
	m=$(hook_field managed-start .systemMessage)
	if is_started managed-start && [[ $m == *"attestation managed"* ]]; then
		result 1 h.attestation "$m"
	else
		result 0 h.attestation "wanted attestation managed: $(hook_evidence managed-start)"
	fi
	enroll_person dave --use-device-code
	result "$(is enrolled_at_managed dave)" h.plain-enroll \
		"straza enroll with no --server, exit $enroll_rc: $(tail -c 400 "$dw/enroll.dave.log")"
}

enrolled_at_managed() {
	[ "$enroll_rc" = 0 ] && grep -q "Enrolled as $1" "$dw/enroll.$1.log" && grep -q "$server_url" "$dw/enroll.$1.log"
}

# wait_link prints the link the BROWSER helper wrote for USER's enroll.
wait_link() {
	local link
	for _ in $(seq 1 300); do
		link=$(cx cat /tmp/rig-browser.link 2>/dev/null)
		if [ -n "$link" ]; then
			printf '%s' "$link"
			return 0
		fi
		kill -0 "${enroll_pid[$1]}" 2>/dev/null || return 1
		sleep 0.2
	done
	return 1
}

# signin_browser LINK USER plays USER's browser at the built-in issuer. It
# opens LINK through the host's port, posts the form's hidden fields with
# the password, and delivers the redirect, which must be the loopback
# callback the form names with a code and the form's state, to the service
# inside the machine through bash's /dev/tcp, because the image has no HTTP
# client. It sets signin_error on a failure and signin_reply to the
# service's status line.
signin_browser() {
	local link=$1 user=$2 page=$dw/authorize.$2.html tag name value status loc state='' redirect_uri='' port
	local -a form=()
	case $link in
	"$server_url"/oidc/authorize\?*) ;;
	*)
		signin_error="the link does not open $server_url/oidc/authorize: ${link%%\?*}"
		return 1
		;;
	esac
	status=$(curl -sS -o "$page" -w '%{http_code}' "$api${link#"$server_url"}")
	if [ "$status" != 200 ] || ! grep -q '<form' "$page"; then
		signin_error="the authorize page answered HTTP $status: $(head -c 300 "$page")"
		return 1
	fi
	while IFS= read -r tag; do
		name=$(sed -nE 's/.*[[:space:]]name="([^"]*)".*/\1/p' <<<"$tag")
		value=$(sed -nE 's/.*[[:space:]]value="([^"]*)".*/\1/p' <<<"$tag")
		form+=(--data-urlencode "$name=$value")
		case $name in redirect_uri) redirect_uri=$value ;; state) state=$value ;; esac
	done < <(grep -oE '<input[^>]*type="hidden"[^>]*>' "$page")
	for _ in 1 2 3 4 5 6 7 8; do
		loc=$(curl -sS -o "$dw/authorize-post.$user.html" -w '%{http_code} %{redirect_url}' -X POST "${form[@]}" \
			--data-urlencode "username=$user" --data-urlencode "password@$dw/$user.pw" "$api/oidc/authorize")
		[ "${loc%% *}" = 429 ] || break
		sleep 2
	done
	status=${loc%% *} loc=${loc#* }
	case $status in 3??) ;; *)
		signin_error="the sign-in form answered HTTP $status instead of a redirect: $(head -c 300 "$dw/authorize-post.$user.html")"
		return 1
		;;
	esac
	if ! [[ $redirect_uri =~ ^http://127\.0\.0\.1:[0-9]+/callback$ ]] || [ "${loc%%\?*}" != "$redirect_uri" ] ||
		! [[ "&${loc#*\?}&" == *"&state=$state&"* && "&${loc#*\?}" == *"&code="?* ]]; then
		signin_error="the issuer redirected to ${loc%%\?*}, want the form's redirect_uri ${redirect_uri:-none} on 127.0.0.1 with a code and the form's state"
		return 1
	fi
	port=${redirect_uri#http://127.0.0.1:} port=${port%%/*}
	# shellcheck disable=SC2016 # the script expands its own arguments
	signin_reply=$(cx bash -c 'exec 3<>"/dev/tcp/127.0.0.1/$1" && printf "GET %s HTTP/1.0\r\nHost: 127.0.0.1:%s\r\n\r\n" "$2" "$1" >&3 && cat <&3' \
		rig "$port" "/${loc#http://127.0.0.1:*/}" 2>&1 | head -1 | tr -d '\r')
	if ! [[ $signin_reply =~ ^HTTP/1\.[01]\ 200 ]]; then
		signin_error="the service's callback on port $port answered: ${signin_reply:-nothing}"
		return 1
	fi
}

# scenario_k enrolls erin by the browser sign-in. BROWSER names a helper in
# the machine that writes every argument it gets to a file, and the rig
# signs erin in with the link it finds there.
scenario_k() {
	local link rc line n
	cat >"$dw/rig-browser" <<-'EOF'
		#!/bin/sh
		printf '%s\n' "$@" >/tmp/rig-browser.tmp && /bin/mv /tmp/rig-browser.tmp /tmp/rig-browser.link
	EOF
	chmod 0755 "$dw/rig-browser"
	docker cp -q "$dw/rig-browser" "$ctr:/usr/local/bin/rig-browser"
	timeout 180 docker exec "$ctr" runuser -u erin -- env BROWSER=/usr/local/bin/rig-browser \
		/usr/bin/straza enroll --server "$server_url" </dev/null >"$dw/enroll.erin.log" 2>&1 &
	enroll_pid[erin]=$!
	if ! link=$(wait_link erin); then
		result 0 k.link "no link reached BROWSER: $(tail -c 400 "$dw/enroll.erin.log")"
		kill "${enroll_pid[erin]}" 2>/dev/null
		wait "${enroll_pid[erin]}"
		return
	fi
	result "$(is test "$(wc -l <<<"$link")" = 1)" k.link "BROWSER got $(wc -l <<<"$link") argument(s), ${link%%\?*}"
	if ! signin_browser "$link" erin; then
		result 0 k.signin "$signin_error"
		kill "${enroll_pid[erin]}" 2>/dev/null
		wait "${enroll_pid[erin]}"
		return
	fi
	result 1 k.signin "the redirect reached the service on 127.0.0.1, which answered $signin_reply"
	wait "${enroll_pid[erin]}"
	rc=$?
	line=$(grep -oE 'Enrolled as [^ ]+ \(device [^)]+\)' "$dw/enroll.erin.log")
	result "$(is test "$rc" = 0 -a -n "$line")" k.enroll "exit $rc: $(tail -c 300 "$dw/enroll.erin.log")"
	n=$(grep -cF "$link" "$dw/enroll.erin.log")
	result "$(is test "$n" -ge 1)" k.printed "the enroll output carries the link on $n line(s)"
	sleep 2 # strazad writes audit records a moment after it answers
	n=$(audit_count '"signIn":"browser"')
	result "$(is test "$n" = 1)" k.record "$n device.enrolled records with signIn browser at strazad"
	hook_bg erin-start erin "$(ss_payload erin-w00 /home/erin)"
	hook_wait
	check_hook k.start started erin-start
}

# scenario_i commits the enrolled machine and boots the image as a clone
# with a fresh machine id, where alice's key must not be used.
scenario_i() {
	local img=$prefix-clone:$distro state main=$ctr
	if ! docker commit "$ctr" "$img" >/dev/null; then
		result 0 i.commit "docker commit of $ctr failed"
		return
	fi
	state=$(start_machine "$clone" "$img" "$(new_machine_id)" "$net")
	if booted "$state"; then
		ctr=$clone
		hook_bg clone-start alice "$(ss_payload alice-clone /home/alice)"
		hook_wait
		check_refused i.clone-start clone-start "This machine's identity changed since it was enrolled"
		hook_bg clone-old-window alice "$(pre_payload alice-w01 /home/alice "git status")"
		hook_wait
		info i.clone-window "a window opened before the commit, asked on the clone: $(hook_evidence clone-old-window)"
		ctr=$main
	else
		result 0 i.clone-boot "the clone did not boot: state ${state:-none}"
	fi
	remove_containers "$clone"
	docker image rm "$img" >/dev/null 2>&1
}

account_kept() {
	local pw
	pw=$(cx getent passwd straza-service)
	result "$(is test -n "$pw")" "$1" "${pw:-the straza-service account is gone}"
}

path_check() {
	local id=$1 want=$2 path=$3 have=present
	absent "$path" && have=absent
	result "$(is test "$have" = "$want")" "$id" "$path is $have"
}

# helper_gone checks that removal took the git-remote-straza symlink, which
# a plain test -e would miss once it dangles.
helper_gone() {
	local left
	left=$(cx sh -c 'ls -l /usr/bin/git-remote-straza 2>/dev/null')
	result "$(is test -z "$left")" "$1" "/usr/bin/git-remote-straza is ${left:-gone}"
}

units_gone() {
	local files listed active
	files=$(cx sh -c 'ls /usr/lib/systemd/system/straza-service.* /etc/systemd/system/sockets.target.wants/straza-service.socket 2>/dev/null')
	listed=$(cx systemctl list-unit-files --no-legend 'straza-service*' 2>&1)
	active=$(cx systemctl is-active straza-service.socket straza-service.service | tr '\n' ' ')
	result "$(is test -z "$files" -a -z "$listed" -a "${active// /}" = inactiveinactive)" "$1" \
		"unit files: ${files:-none}, listed: ${listed:-none}, states: $active"
}

scenario_j() {
	local out rc
	if [ "$fam" = deb ]; then
		out=$(cx env DEBIAN_FRONTEND=noninteractive apt-get remove -y -q straza 2>&1)
		rc=$?
		result "$(is test "$rc" = 0)" j.remove "exit $rc: $(tr "\r" "\n" <<<"$out" | tail -2)"
		account_kept j.remove-account
		path_check j.remove-state present /var/lib/straza-service
		path_check j.remove-run absent /run/straza-service
		units_gone j.remove-units
		helper_gone j.remove-helper
		out=$(cx env DEBIAN_FRONTEND=noninteractive apt-get purge -y -q straza 2>&1)
		rc=$?
		result "$(is test "$rc" = 0)" j.purge "exit $rc: $(tr "\r" "\n" <<<"$out" | tail -2)"
		path_check j.purge-state absent /var/lib/straza-service
		account_kept j.purge-account
	else
		out=$(cx rpm -e straza 2>&1)
		rc=$?
		result "$(is test "$rc" = 0)" j.erase "exit $rc: $out"
		path_check j.erase-state absent /var/lib/straza-service
		path_check j.erase-run absent /run/straza-service
		account_kept j.erase-account
		units_gone j.erase-units
		helper_gone j.erase-helper
	fi
}

teardown() {
	local keep=$1
	[ -n "$ctr" ] || return 0
	if docker inspect "$ctr" >/dev/null 2>&1; then
		cx journalctl --no-pager -o short-iso -u straza-service.service -u straza-service.socket >"$dw/journal.txt" 2>&1
	fi
	docker logs "$sd" 2>&1 | redact >"$dw/strazad.log"
	if [ "$keep" = 1 ]; then
		say "kept $ctr, $sd and $prefix-sd:$distro for inspection. 'run.sh clean' removes them."
		return
	fi
	remove_containers "$ctr" "$sd" "$clone"
	docker network rm "$net" >/dev/null 2>&1
	drop_images "$prefix-clone:$distro" "$prefix-sd:$distro"
}

run_distro() {
	fam=$(family_of "$distro")
	dw=$work/$distro hd=$work/$distro/hooks
	ctr=$prefix-$distro net=$prefix-net-$distro sd=$prefix-strazad-$distro clone=$prefix-clone-$distro
	mkdir -p "$hd"
	remove_containers "$ctr" "$sd" "$clone"
	docker network rm "$net" >/dev/null 2>&1
	if ! find_pkgs; then
		result 0 setup.packages "PKGDIR needs exactly two straza $fam packages for amd64, and it holds:" \
			"$(find "$pkgdir" -maxdepth 1 -type f -printf '%f ')"
		return
	fi
	if ! docker image inspect "$prefix-sd:$distro" >/dev/null 2>&1 && ! build_image "$distro"; then
		result 0 setup.image "image build failed: $(tail -5 "$work/build-$distro.log")"
		return
	fi
	docker network create --label "$label" "$net" >/dev/null
	scenario_a || return
	machine_up || return
	scenario_b || return
	scenario_cd
	scenario_e
	scenario_f
	scenario_g
	scenario_h
	scenario_k
	scenario_i
	scenario_j
}

cmd_test() {
	local keep=0 d
	if [ "${1:-}" = --keep ]; then
		keep=1
		shift
	fi
	[ $# -ge 1 ] || die "test needs PKGDIR. Run 'run.sh help' for its layout and the commands that make it."
	pkgdir=$(cd "$1" 2>/dev/null && pwd) || die "PKGDIR $1 is not a directory. Run 'run.sh help' for its layout."
	shift
	strazad_bin=$pkgdir/strazad
	[ -x "$strazad_bin" ] || die "PKGDIR $pkgdir has no executable strazad. Copy the linux amd64 strazad of the newer build into it."
	pick_distros "$@"
	make_work
	trap 'hook_wait; teardown 0; exit 130' INT TERM
	for d in "${sel[@]}"; do
		distro=$d
		disk_guard || break
		say "testing $d, disk $(disk_pct)%"
		run_distro
		teardown "$keep"
	done
	trap - INT TERM
}

main() {
	command -v docker >/dev/null || die "docker is not on PATH. Install Docker, then run this again."
	command -v jq >/dev/null || die "jq is not on PATH. Install jq, then run this again."
	command -v curl >/dev/null || die "curl is not on PATH. Install curl, then run this again."
	umask 077
	local cmd=${1:-help}
	[ $# -gt 0 ] && shift
	case $cmd in
	boot | test)
		"cmd_$cmd" "$@"
		printf 'SUMMARY %s passed, %s failed, work directory %s\n' "$npass" "$nfail" "$work"
		[ "$nfail" = 0 ]
		;;
	clean) cmd_clean "$@" ;;
	help | -h | --help) usage ;;
	*)
		usage >&2
		exit 2
		;;
	esac
}

main "$@"
