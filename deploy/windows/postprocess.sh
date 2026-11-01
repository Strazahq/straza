#!/bin/sh
# postprocess.sh adds to the MSI what wixl 0.106 cannot write, with msibuild.
# It runs inside build-msi.sh's container against the freshly built MSI named
# by $1, an absolute path, with $2 the same straza.exe wixl packed. Each change
# is one assert.sh then checks, so a drift here fails the build.
set -eu
M=$1
EXE=$2

# wixl drops the vital bit, so the service install reads ErrorControl normal.
# 32769 is normal (1) plus vital (0x8000): a failed CreateService rolls the
# install back instead of leaving a half-installed service.
msibuild "$M" -q "UPDATE \`ServiceInstall\` SET \`ErrorControl\`=32769 WHERE \`ServiceInstall\`='StrazaService'"

# The SID type and the failure flag, which wixl has no element for. Same
# values as the 3A install script: unrestricted SID, and the failure flag so
# a failed start counts. Event 5 applies them on install and repair, never
# on uninstall, because the uninstall deletes the service first (2000) and a
# change to a deleted service fails the uninstall with error 1939. The
# restart actions come from the StrazaRecovery custom action, because
# MsiServiceConfigFailureActions fails with access denied for a restart.
msibuild "$M" -q "CREATE TABLE \`MsiServiceConfig\` (\`MsiServiceConfig\` CHAR(72) NOT NULL, \`Name\` CHAR(255) NOT NULL LOCALIZABLE, \`Event\` SHORT NOT NULL, \`ConfigType\` LONG NOT NULL, \`Argument\` LONGCHAR, \`Component_\` CHAR(72) NOT NULL PRIMARY KEY \`MsiServiceConfig\`)"
msibuild "$M" -q "INSERT INTO \`MsiServiceConfig\` (\`MsiServiceConfig\`,\`Name\`,\`Event\`,\`ConfigType\`,\`Argument\`,\`Component_\`) VALUES ('SidType','straza-service',5,5,'1','StrazaExe')"
msibuild "$M" -q "INSERT INTO \`MsiServiceConfig\` (\`MsiServiceConfig\`,\`Name\`,\`Event\`,\`ConfigType\`,\`Argument\`,\`Component_\`) VALUES ('FailFlag','straza-service',5,4,'1','StrazaExe')"

# MsiConfigureServices applies the table above. It runs after
# InstallServices (5800) and before StartServices (5900).
msibuild "$M" -q "INSERT INTO \`InstallExecuteSequence\` (\`Action\`,\`Sequence\`) VALUES ('MsiConfigureServices',5810)"

# wixl hardcodes RemoveExistingProducts at 1401, right after InstallValidate,
# which would strip the old product's exe and service before the new ones
# land. Move it after StartServices (5900) and before InstallFinalize (6600):
# the new files and service are in place first, and the shared, stable-GUID
# component is reference-counted, so the old product's removal never takes the
# exe. That is what keeps straza.exe present through the whole upgrade.
# Microsoft allows this late slot only between InstallExecute and
# InstallFinalize, and error 2613 is a RemoveExistingProducts sequenced
# anywhere else. InstallExecute (6490) runs the script built so far, every
# deferred action here included, so the new product's files, service and
# component registrations are written before the old product's removal counts
# who still uses them. A failed removal then rolls both products back.
msibuild "$M" -q "UPDATE \`InstallExecuteSequence\` SET \`Sequence\`=6500 WHERE \`Action\`='RemoveExistingProducts'"
msibuild "$M" -q "INSERT INTO \`InstallExecuteSequence\` (\`Action\`,\`Sequence\`) VALUES ('InstallExecute',6490)"

# A deferred action reads a public property only when it is secure. wixl drops
# the Secure attribute, so PURGE joins the list wixl wrote for its own upgrade
# properties.
msibuild "$M" -q "UPDATE \`Property\` SET \`Value\`='WIX_UPGRADE_DETECTED;WIX_DOWNGRADE_DETECTED;WIX_SAME_VERSION_UPGRADE_DETECTED;PURGE' WHERE \`Property\`='SecureCustomProperties'"

# The machine PATH entry for C:\ProgramData\straza\bin\, so git finds
# git-remote-straza.exe. wixl's Environment element sets the remove flag
# backwards and keys the row with a random id, so the row is written here.
# = sets it on install, - removes it when the GitRemote component goes, *
# writes the machine environment, and [~]; appends to the PATH already there.
# A PATH entry left after the uninstall would name a folder that any user
# could create once an administrator deleted it. RemoveEnvironmentStrings
# runs before RemoveFiles (3500). WriteEnvironmentStrings runs after the first
# lock (3550) and InstallFiles (4000), so the entry never names a folder a
# user could write. wixl sequences neither without an Environment element.
msibuild "$M" -q "INSERT INTO \`Environment\` (\`Environment\`,\`Name\`,\`Value\`,\`Component_\`) VALUES ('StrazaBinPath','=-*PATH','[~];[BINDIR]','GitRemote')"
msibuild "$M" -q "INSERT INTO \`InstallExecuteSequence\` (\`Action\`,\`Sequence\`) VALUES ('RemoveEnvironmentStrings',3300)"
msibuild "$M" -q "INSERT INTO \`InstallExecuteSequence\` (\`Action\`,\`Sequence\`) VALUES ('WriteEnvironmentStrings',5200)"

# The first lock. StrazaLock at 5850 runs the installed exe, which sits in a
# folder a standard user could have created before the install and so could
# swap the exe in. This action runs `installer lock` from a second copy of the
# same exe in the Binary table instead: Windows Installer extracts it for a
# deferred, not impersonated action into its own admin-only folder, so nothing
# a user can write runs as SYSTEM. It runs before any file or folder lands
# (InstallFiles 4000), so the lock creates the root locked or refuses a root a
# non-administrator owns, and Windows rolls the install back before a file is
# written. 3074 is an exe from the Binary table (2), deferred (0x400) and not
# impersonated (0x800), with Return check.
# wixl writes no Binary rows here, so importing the table replaces nothing.
# msibuild resolves the stream file against the working directory, so the
# import runs from the IDT folder.
idt=$(mktemp -d)
mkdir "$idt/Binary"
cp "$EXE" "$idt/Binary/StrazaLockExe.ibd"
printf 'Name\tData\r\ns72\tv0\r\nBinary\tName\r\nStrazaLockExe\tStrazaLockExe.ibd\r\n' >"$idt/Binary.idt"
(cd "$idt" && msibuild "$M" -i Binary.idt)
rm -rf "$idt"
msibuild "$M" -q "INSERT INTO \`CustomAction\` (\`Action\`,\`Type\`,\`Source\`,\`Target\`) VALUES ('StrazaLockEarly',3074,'StrazaLockExe','installer lock')"
msibuild "$M" -q "INSERT INTO \`InstallExecuteSequence\` (\`Action\`,\`Condition\`,\`Sequence\`) VALUES ('StrazaLockEarly','NOT REMOVE~=\"ALL\"',3550)"

# The uninstall's unwire and purge. Both change what Windows Installer cannot
# roll back: the harnesses' managed settings and the service's keys. As commit
# actions they run only once the whole uninstall has succeeded, so an
# uninstall that fails and rolls back leaves the harnesses wired and the keys
# in place, never a reinstated service beside ungoverned harnesses. By then
# RemoveFiles has deleted the installed exe, so they run the Binary copy.
# 3650 is an exe from the Binary table (2), Return ignore (0x40), deferred
# (0x400), commit (0x200) and not impersonated (0x800). Windows runs no
# commit action while rollback is disabled, so the Now twins, 3138, run in
# the uninstall script itself under RollbackDisabled, where a failure leaves a
# half-removed machine anyway. Return ignore keeps a failed unwire or purge
# from undoing a finished uninstall, and the slots keep the unwire first.
msibuild "$M" -q "INSERT INTO \`CustomAction\` (\`Action\`,\`Type\`,\`Source\`,\`Target\`) VALUES ('StrazaUninstall',3650,'StrazaLockExe','uninstall --managed claude-code codex gemini')"
msibuild "$M" -q "INSERT INTO \`CustomAction\` (\`Action\`,\`Type\`,\`Source\`,\`Target\`) VALUES ('StrazaUninstallNow',3138,'StrazaLockExe','uninstall --managed claude-code codex gemini')"
msibuild "$M" -q "INSERT INTO \`CustomAction\` (\`Action\`,\`Type\`,\`Source\`,\`Target\`) VALUES ('StrazaPurge',3650,'StrazaLockExe','installer purge')"
msibuild "$M" -q "INSERT INTO \`CustomAction\` (\`Action\`,\`Type\`,\`Source\`,\`Target\`) VALUES ('StrazaPurgeNow',3138,'StrazaLockExe','installer purge')"
msibuild "$M" -q "INSERT INTO \`InstallExecuteSequence\` (\`Action\`,\`Condition\`,\`Sequence\`) VALUES ('StrazaUninstall','REMOVE~=\"ALL\" AND NOT UPGRADINGPRODUCTCODE AND NOT RollbackDisabled',3400)"
msibuild "$M" -q "INSERT INTO \`InstallExecuteSequence\` (\`Action\`,\`Condition\`,\`Sequence\`) VALUES ('StrazaUninstallNow','REMOVE~=\"ALL\" AND NOT UPGRADINGPRODUCTCODE AND RollbackDisabled',3401)"
msibuild "$M" -q "INSERT INTO \`InstallExecuteSequence\` (\`Action\`,\`Condition\`,\`Sequence\`) VALUES ('StrazaPurge','REMOVE~=\"ALL\" AND PURGE=\"1\" AND NOT UPGRADINGPRODUCTCODE AND NOT RollbackDisabled',3450)"
msibuild "$M" -q "INSERT INTO \`InstallExecuteSequence\` (\`Action\`,\`Condition\`,\`Sequence\`) VALUES ('StrazaPurgeNow','REMOVE~=\"ALL\" AND PURGE=\"1\" AND NOT UPGRADINGPRODUCTCODE AND RollbackDisabled',3451)"
