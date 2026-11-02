#!/bin/sh
# assert.sh fails the build when the MSI's tables drift from what units 3B
# and 5B fixed. It reads the tables with msiinfo export, so it runs inside the
# msitools container that container-build.sh and the test rig start. It
# checks every sequenced action, the service tables, the custom actions, the
# upgrade rows, the git remote helper and its PATH entry, and that the
# license files are carried.
#
#   assert.sh <msi> <expected-product-version>
set -u
M=$1
WANT_VER=$2
fail=0
tmp=$(mktemp -d)

# msiinfo export writes CRLF, so strip the carriage return that would
# otherwise cling to every last field.
dump() { msiinfo export "$M" "$1" 2>/dev/null | tr -d '\r' | tail -n +4 >"$tmp/$1"; }
for t in InstallExecuteSequence ServiceInstall ServiceControl CustomAction \
	Component MsiServiceConfig Property File \
	MsiFileHash LaunchCondition Upgrade Binary FeatureComponents Environment; do
	dump "$t"
done

# field prints column c of the row whose first column is k, or exits non-zero.
field() { awk -F'\t' -v k="$2" -v c="$3" '$1==k{print $c; f=1} END{exit !f}' "$tmp/$1"; }

# eq asserts column c of row k in table equals want.
eq() { # table key col want label
	got=$(field "$1" "$2" "$3") || {
		echo "DRIFT: $1 has no row $2 ($5)"
		fail=1
		return
	}
	[ "$got" = "$4" ] || {
		echo "DRIFT: $1 $2 col$3 is [$got], want [$4] ($5)"
		fail=1
	}
}

# has asserts the table holds a literal substring.
has() { # table literal label
	grep -qF "$2" "$tmp/$1" || {
		echo "DRIFT: $1 lacks [$2] ($3)"
		fail=1
	}
}

# --- The sequenced actions. The order is the install's and the upgrade's
# safety: the root is locked from the Binary copy (3550) before any file lands
# (4000), the service stops (1900) before the exe is replaced, the files are
# re-owned (5850) before the service starts (5900), and the old product is
# removed (6500) only after all of that and before the commit (6600), so the
# exe is never absent. InstallExecute (6490) runs the new product's script
# right before that removal, the only late slot Windows allows (error 2613).
# The machine PATH entry goes (3300) before the files and is written (5200)
# only after the first lock and the files, so it never names a folder a user
# could write. The two PATH actions and InstallExecute carry no condition.
# The unwire and the purge wait for the commit, so an uninstall that rolls
# back never leaves the service reinstated beside unwired harnesses. ---
eq InstallExecuteSequence StopServices 3 1900 "stop before replace"
eq InstallExecuteSequence DeleteServices 3 2000 "delete after stop"
eq InstallExecuteSequence RemoveEnvironmentStrings 3 3300 "PATH entry removed before the files"
eq InstallExecuteSequence RemoveEnvironmentStrings 2 '' "PATH entry removal unconditional"
eq InstallExecuteSequence StrazaUninstall 3 3400 "unwire slot"
eq InstallExecuteSequence StrazaUninstall 2 'REMOVE~="ALL" AND NOT UPGRADINGPRODUCTCODE AND NOT RollbackDisabled' "unwire on uninstall not upgrade, at commit"
eq InstallExecuteSequence StrazaUninstallNow 3 3401 "unwire twin slot"
eq InstallExecuteSequence StrazaUninstallNow 2 'REMOVE~="ALL" AND NOT UPGRADINGPRODUCTCODE AND RollbackDisabled' "unwire twin only while rollback is disabled"
eq InstallExecuteSequence StrazaPurge 3 3450 "purge slot, after the unwire"
eq InstallExecuteSequence StrazaPurge 2 'REMOVE~="ALL" AND PURGE="1" AND NOT UPGRADINGPRODUCTCODE AND NOT RollbackDisabled' "purge condition, at commit"
eq InstallExecuteSequence StrazaPurgeNow 3 3451 "purge twin slot"
eq InstallExecuteSequence StrazaPurgeNow 2 'REMOVE~="ALL" AND PURGE="1" AND NOT UPGRADINGPRODUCTCODE AND RollbackDisabled' "purge twin only while rollback is disabled"
eq InstallExecuteSequence RemoveFiles 3 3500 "remove files slot"
eq InstallExecuteSequence StrazaLockEarly 3 3550 "first lock before any file lands"
eq InstallExecuteSequence StrazaLockEarly 2 'NOT REMOVE~="ALL"' "first lock on install and upgrade"
eq InstallExecuteSequence InstallFiles 3 4000 "install files slot"
eq InstallExecuteSequence WriteEnvironmentStrings 3 5200 "PATH entry written after the first lock and the files"
eq InstallExecuteSequence WriteEnvironmentStrings 2 '' "PATH entry write unconditional"
eq InstallExecuteSequence InstallServices 3 5800 "install services slot"
eq InstallExecuteSequence MsiConfigureServices 3 5810 "configure services added"
eq InstallExecuteSequence StrazaLock 3 5850 "lock after files before start"
eq InstallExecuteSequence StrazaLock 2 'NOT REMOVE~="ALL"' "lock on install and upgrade"
eq InstallExecuteSequence StrazaRecovery 3 5860 "restart actions after the service exists, before it starts"
eq InstallExecuteSequence StrazaRecovery 2 'NOT REMOVE~="ALL"' "restart actions on install and upgrade"
eq InstallExecuteSequence StartServices 3 5900 "start services slot"
eq InstallExecuteSequence InstallExecute 3 6490 "new product's script runs before the old product's removal"
eq InstallExecuteSequence InstallExecute 2 '' "InstallExecute unconditional"
eq InstallExecuteSequence RemoveExistingProducts 3 6500 "RExP late, after start before finalize"
if ie=$(field InstallExecuteSequence InstallExecute 3) && rexp=$(field InstallExecuteSequence RemoveExistingProducts 3); then
	between=$(awk -F'\t' -v a="$ie" -v b="$rexp" '$3+0>a+0 && $3+0<b+0{printf "%s ", $1}' "$tmp/InstallExecuteSequence")
	if [ "$ie" -ge "$rexp" ] || [ -n "$between" ]; then
		echo "DRIFT: InstallExecute [$ie] must come right before RemoveExistingProducts [$rexp], with nothing between [$between] (error 2613)"
		fail=1
	fi
fi
eq InstallExecuteSequence InstallFinalize 3 6600 "finalize slot"

# --- The service, with the values the 3A install script set. ---
eq ServiceInstall StrazaService 2 straza-service "service name"
eq ServiceInstall StrazaService 4 16 "own process"
eq ServiceInstall StrazaService 5 2 "auto start"
eq ServiceInstall StrazaService 6 32769 "error control vital"
eq ServiceInstall StrazaService 9 'NT SERVICE\straza-service' "service account"
eq ServiceInstall StrazaService 11 service "service arguments"
eq ServiceInstall StrazaService 12 StrazaExe "service component"
eq ServiceControl StrazaServiceControl 2 straza-service "control name"
eq ServiceControl StrazaServiceControl 3 163 "start install, stop both, delete uninstall"
eq ServiceControl StrazaServiceControl 6 StrazaExe "control component"
eq MsiServiceConfig SidType 3 5 "sid type on install and repair, never on uninstall"
eq MsiServiceConfig SidType 4 5 "config type sid"
eq MsiServiceConfig SidType 5 1 "sid type unrestricted"
eq MsiServiceConfig FailFlag 3 5 "fail flag on install and repair, never on uninstall"
eq MsiServiceConfig FailFlag 4 4 "config type fail flag"
eq MsiServiceConfig FailFlag 5 1 "fail flag on"
# The restart actions come from StrazaRecovery. The failure-actions table
# fails a restart action with access denied, so it must stay out.
if msiinfo tables "$M" 2>/dev/null | tr -d '\r' | grep -qx MsiServiceConfigFailureActions; then
	echo "DRIFT: the MSI carries MsiServiceConfigFailureActions, which fails every install with error 1939 (StrazaRecovery sets the restart actions)"
	fail=1
fi

# --- The custom actions call lane C's commands, deferred and not
# impersonated (3090 = Return check). ---
eq CustomAction StrazaLock 2 3090 "lock type: deferred no-impersonate exe, return check"
eq CustomAction StrazaLock 3 StrazaExe "lock runs the installed exe"
eq CustomAction StrazaLock 4 'installer lock' "lock command line"
eq CustomAction StrazaRecovery 2 3090 "recovery type: deferred no-impersonate exe, return check"
eq CustomAction StrazaRecovery 3 StrazaExe "recovery runs the installed exe"
eq CustomAction StrazaRecovery 4 'installer recovery' "recovery command line"
eq CustomAction StrazaLockEarly 2 3074 "first lock type: deferred no-impersonate exe from the Binary table, return check"
eq CustomAction StrazaLockEarly 3 StrazaLockExe "first lock runs the Binary copy"
eq CustomAction StrazaLockEarly 4 'installer lock' "first lock command line"
# The unwire and the purge run at commit (3650), after the uninstall
# succeeded, from the Binary copy, because RemoveFiles took the installed exe.
# Their twins (3138) run deferred while rollback is disabled.
for a in StrazaUninstall StrazaUninstallNow StrazaPurge StrazaPurgeNow; do
	eq CustomAction "$a" 3 StrazaLockExe "$a runs the Binary copy"
done
eq CustomAction StrazaUninstall 2 3650 "unwire type: commit no-impersonate exe from the Binary table, return ignore"
eq CustomAction StrazaUninstallNow 2 3138 "unwire twin type: deferred no-impersonate exe from the Binary table, return ignore"
eq CustomAction StrazaPurge 2 3650 "purge type: commit no-impersonate exe from the Binary table, return ignore"
eq CustomAction StrazaPurgeNow 2 3138 "purge twin type: deferred no-impersonate exe from the Binary table, return ignore"
for a in StrazaUninstall StrazaUninstallNow; do
	eq CustomAction "$a" 4 'uninstall --managed claude-code codex gemini' "$a command line"
done
for a in StrazaPurge StrazaPurgeNow; do
	eq CustomAction "$a" 4 'installer purge' "$a command line"
done

# --- The upgrade mechanism. ---
eq Component StrazaExe 2 '{9125B44D-BEBA-4C50-BF35-04FA01BB14BB}' "frozen component GUID"
has MsiFileHash StrazaExe "unversioned exe carries a content hash"
has LaunchCondition 'NOT WIX_DOWNGRADE_DETECTED' "downgrade refused"
has Upgrade WIX_SAME_VERSION_UPGRADE_DETECTED "same-version upgrade allowed"

# --- The git remote helper, a second copy of straza.exe in its own component
# whose GUID is frozen like StrazaExe's, because the late removal of the old
# product would otherwise take the helper and the PATH entry. The PATH row is
# the only Environment row: =-* sets the machine PATH on install and removes
# the entry with the component, and [~]; appends to the PATH already there. ---
eq File GitRemoteExe 2 GitRemote "helper in its own component"
eq File GitRemoteExe 3 git-remote-straza.exe "helper name git looks up"
eq Component GitRemote 2 '{588DC863-DE07-4B24-B0DE-0176FFBD4FF6}' "frozen helper component GUID"
eq Component GitRemote 3 BINDIR "helper in the locked bin folder"
eq Component GitRemote 4 256 "helper component is 64-bit"
eq Component GitRemote 6 GitRemoteExe "helper file is the key path"
awk -F'\t' '$1=="Main" && $2=="GitRemote"{f=1} END{exit !f}' "$tmp/FeatureComponents" || {
	echo "DRIFT: FeatureComponents has no row Main GitRemote (helper installed with the feature)"
	fail=1
}
for c in 3 4 5 6; do
	eq MsiFileHash GitRemoteExe "$c" "$(field MsiFileHash StrazaExe "$c")" "helper carries straza.exe's content hash"
done
eq Environment StrazaBinPath 2 '=-*PATH' "machine PATH, set on install, removed with the component"
eq Environment StrazaBinPath 3 '[~];[BINDIR]' "bin folder appended to PATH"
eq Environment StrazaBinPath 4 GitRemote "PATH entry belongs to the helper component"
env_rows=$(wc -l <"$tmp/Environment")
[ "$env_rows" -eq 1 ] || {
	echo "DRIFT: Environment holds $env_rows rows, want only StrazaBinPath (one PATH row)"
	fail=1
}

# --- The properties the deferred actions and Intune need. ---
eq Property ProductVersion 2 "$WANT_VER" "product version"
eq Property MSIRESTARTMANAGERCONTROL 2 Disable "restart manager disabled"
has Property ';PURGE' "PURGE is a secure property"

# --- The license and notice files the MSI carries, like the deb. ---
for f in LICENSE LICENSE-APACHE NOTICE THIRD_PARTY_NOTICES.txt; do
	has File "$f" "carries $f"
done

# --- The Binary copy the first lock runs: its row, its stream, and the same
# bytes as the straza.exe the File table installs. ---
has Binary StrazaLockExe "Binary row for the first lock"
msiinfo streams "$M" 2>/dev/null | tr -d '\r' | grep -qx 'Binary.StrazaLockExe' || {
	echo "DRIFT: the MSI has no Binary.StrazaLockExe stream (first lock)"
	fail=1
}
bin_sum=$(msiinfo extract "$M" Binary.StrazaLockExe 2>/dev/null | sha256sum | cut -d' ' -f1)
mkdir "$tmp/x"
msiextract -C "$tmp/x" "$M" >/dev/null 2>&1
installed=$(find "$tmp/x" -name straza.exe | head -n 1)
file_sum=$( [ -n "$installed" ] && sha256sum "$installed" | cut -d' ' -f1)
if [ -z "$file_sum" ] || [ "$bin_sum" != "$file_sum" ]; then
	echo "DRIFT: the Binary copy's sha256 [$bin_sum] is not the installed straza.exe's [$file_sum]"
	fail=1
fi
# The helper is a copy of straza.exe, so it must carry the same bytes.
helper=$(find "$tmp/x" -name git-remote-straza.exe | head -n 1)
helper_sum=$( [ -n "$helper" ] && sha256sum "$helper" | cut -d' ' -f1)
if [ -z "$helper_sum" ] || [ "$helper_sum" != "$file_sum" ]; then
	echo "DRIFT: git-remote-straza.exe's sha256 [$helper_sum] is not the installed straza.exe's [$file_sum]"
	fail=1
fi

rm -rf "$tmp"
if [ "$fail" -ne 0 ]; then
	echo "assert.sh: the MSI drifted from the tables units 3B and 5B fixed. Fix the .wxs or postprocess.sh." >&2
	exit 1
fi
echo "assert.sh: every sequenced action, service table, custom action and upgrade row matches."
