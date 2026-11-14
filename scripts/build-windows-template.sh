#!/usr/bin/env bash
# Build a Windows Server template image for this panel, on any KVM host.
#
# Microsoft does not allow a Windows image to be redistributed, so the panel
# ships none: each operator builds their own with this script. It installs
# Windows unattended into a fresh disk, adds what a VM here needs (virtio
# drivers, the QEMU guest agent, cloudbase-init reading the same setup disk a
# Linux VM gets), turns on Remote Desktop, generalises the system with sysprep
# and writes a compressed qcow2.
#
# By default it downloads Microsoft's evaluation media, which runs 180 days
# unactivated. A customer activates their VM with their own key:
#     DISM /Online /Set-Edition:ServerStandard /ProductKey:XXXXX-... /AcceptEula
# Pass your own ISO with --iso to build from licensed media instead.
#
# Needs: qemu-system-x86_64 with KVM, qemu-img, xorriso, curl; about 6 GB of
# RAM and 45 GB of disk while it runs. Nothing is installed on the host and no
# libvirt domain is created. Takes about fifteen minutes once the media (5 to
# 6 GB) is downloaded.
#
# Usage: build-windows-template.sh --version 2022 --out /var/lib/libvirt/templates/windows-2022.qcow2
#          [--iso PATH_OR_URL] [--edition standard|datacenter] [--core] [--work DIR] [--keep]
set -euo pipefail

VERSION="" OUT="" ISO="" EDITION=standard CORE=0 WORK="" KEEP=0
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --iso) ISO="$2"; shift 2 ;;
    --edition) EDITION="$2"; shift 2 ;;
    --core) CORE=1; shift ;;
    --work) WORK="$2"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    -h|--help) sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done
[ -n "$VERSION" ] && [ -n "$OUT" ] || { echo "usage: $0 --version 2019|2022|2025 --out FILE.qcow2 (see --help)" >&2; exit 2; }

# Microsoft's evaluation downloads, and the folder each version's drivers sit
# in on the virtio-win disc.
case "$VERSION" in
  2019) EVAL_URL='https://go.microsoft.com/fwlink/p/?LinkID=2195167&clcid=0x409&culture=en-us&country=US'; DRV=2k19 ;;
  2022) EVAL_URL='https://go.microsoft.com/fwlink/p/?LinkID=2195280&clcid=0x409&culture=en-us&country=US'; DRV=2k22 ;;
  2025) EVAL_URL='https://go.microsoft.com/fwlink/?linkid=2293312&clcid=0x409&culture=en-us&country=us'; DRV=2k25 ;;
  *) echo "unsupported version: $VERSION (2019, 2022 or 2025)" >&2; exit 2 ;;
esac
VIRTIO_URL='https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/stable-virtio/virtio-win.iso'
CLOUDBASE_URL='https://cloudbase.it/downloads/CloudbaseInitSetup_Stable_x64.msi'

# The install image's index inside the media: Standard and Datacenter, each
# without and with the desktop.
case "$EDITION" in
  standard) INDEX=$((2 - CORE)) ;;
  datacenter) INDEX=$((4 - CORE)) ;;
  *) echo "unsupported edition: $EDITION (standard or datacenter)" >&2; exit 2 ;;
esac

for tool in qemu-system-x86_64 qemu-img xorriso curl; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done
[ -w /dev/kvm ] || { echo "/dev/kvm is not usable; this needs hardware virtualisation" >&2; exit 1; }
[ ! -e "$OUT" ] || { echo "$OUT already exists" >&2; exit 1; }

WORK="${WORK:-$(dirname "$OUT")/.windows-build-$VERSION}"
mkdir -p "$WORK/cd"
cleanup() { [ "$KEEP" = 1 ] || rm -rf "$WORK"; }
trap cleanup EXIT

fetch() { # url-or-path, destination
  if [ -f "$1" ]; then ln -sf "$(readlink -f "$1")" "$2"; return; fi
  [ -s "$2" ] && return
  echo "Downloading $(basename "$2")"
  curl -fL --retry 5 --retry-all-errors -C - -o "$2.part" "$1" && mv "$2.part" "$2"
}
fetch "${ISO:-$EVAL_URL}" "$WORK/windows.iso"
fetch "$VIRTIO_URL" "$WORK/virtio-win.iso"
fetch "$CLOUDBASE_URL" "$WORK/cd/CloudbaseInitSetup.msi"

# The Administrator password during the build. It never leaves this run: the
# finished image gets a new one from the panel on each VM's first boot, and if
# that ever failed, nobody could sign in with this one either.
BUILD_PW="$(head -c 18 /dev/urandom | base64 | tr -d '/+=' | cut -c1-16)aA1!"

# Setup is told where the storage and network drivers are for every drive
# letter the driver disc might get.
driver_paths() {
  local n=0
  for letter in D E F G; do
    for drv in viostor vioscsi NetKVM; do
      n=$((n + 1))
      printf '        <PathAndCredentials wcm:action="add" wcm:keyValue="%d"><Path>%s:\\%s\\%s\\amd64</Path></PathAndCredentials>\n' "$n" "$letter" "$drv" "$DRV"
    done
  done
}

cat > "$WORK/cd/autounattend.xml" <<XML
<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">
  <settings pass="windowsPE">
    <component name="Microsoft-Windows-International-Core-WinPE" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <SetupUILanguage><UILanguage>en-US</UILanguage></SetupUILanguage>
      <InputLocale>en-US</InputLocale><SystemLocale>en-US</SystemLocale><UILanguage>en-US</UILanguage><UserLocale>en-US</UserLocale>
    </component>
    <component name="Microsoft-Windows-PnpCustomizationsWinPE" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <DriverPaths>
$(driver_paths)
      </DriverPaths>
    </component>
    <component name="Microsoft-Windows-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <DiskConfiguration>
        <Disk wcm:action="add">
          <DiskID>0</DiskID>
          <WillWipeDisk>true</WillWipeDisk>
          <CreatePartitions>
            <CreatePartition wcm:action="add"><Order>1</Order><Type>Primary</Type><Size>350</Size></CreatePartition>
            <CreatePartition wcm:action="add"><Order>2</Order><Type>Primary</Type><Extend>true</Extend></CreatePartition>
          </CreatePartitions>
          <ModifyPartitions>
            <ModifyPartition wcm:action="add"><Order>1</Order><PartitionID>1</PartitionID><Format>NTFS</Format><Label>System</Label><Active>true</Active></ModifyPartition>
            <ModifyPartition wcm:action="add"><Order>2</Order><PartitionID>2</PartitionID><Format>NTFS</Format><Label>Windows</Label><Letter>C</Letter></ModifyPartition>
          </ModifyPartitions>
        </Disk>
      </DiskConfiguration>
      <ImageInstall>
        <OSImage>
          <InstallFrom><MetaData wcm:action="add"><Key>/IMAGE/INDEX</Key><Value>$INDEX</Value></MetaData></InstallFrom>
          <InstallTo><DiskID>0</DiskID><PartitionID>2</PartitionID></InstallTo>
        </OSImage>
      </ImageInstall>
      <UserData>
        <AcceptEula>true</AcceptEula>
        <ProductKey><WillShowUI>OnError</WillShowUI></ProductKey>
      </UserData>
    </component>
  </settings>
  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <OOBE>
        <HideEULAPage>true</HideEULAPage>
        <HideLocalAccountScreen>true</HideLocalAccountScreen>
        <HideOnlineAccountScreens>true</HideOnlineAccountScreens>
        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>
        <ProtectYourPC>3</ProtectYourPC>
        <SkipMachineOOBE>true</SkipMachineOOBE>
        <SkipUserOOBE>true</SkipUserOOBE>
      </OOBE>
      <UserAccounts>
        <AdministratorPassword><Value>$BUILD_PW</Value><PlainText>true</PlainText></AdministratorPassword>
      </UserAccounts>
      <AutoLogon>
        <Enabled>true</Enabled><Username>Administrator</Username><LogonCount>1</LogonCount>
        <Password><Value>$BUILD_PW</Value><PlainText>true</PlainText></Password>
      </AutoLogon>
      <FirstLogonCommands>
        <SynchronousCommand wcm:action="add">
          <Order>1</Order>
          <CommandLine>cmd /c for %d in (D E F G H) do if exist %d:\\maburvm-setup.ps1 powershell -NoProfile -ExecutionPolicy Bypass -File %d:\\maburvm-setup.ps1</CommandLine>
        </SynchronousCommand>
      </FirstLogonCommands>
    </component>
  </settings>
</unattend>
XML

# cloudbase-init reads the "cidata" disc the panel attaches to every VM (the
# same one cloud-init reads on Linux): the name, the static address, and a
# user-data that sets the Administrator password. It is given no plugin that
# would invent a password of its own.
cloudbase_conf() { # the plugin list
  cat <<CONF
[DEFAULT]
username=Administrator
groups=Administrators
first_logon_behaviour=no
config_drive_raw_hhd=true
config_drive_cdrom=true
config_drive_vfat=true
bsdtar_path=C:\\Program Files\\Cloudbase Solutions\\Cloudbase-Init\\bin\\bsdtar.exe
mtools_path=C:\\Program Files\\Cloudbase Solutions\\Cloudbase-Init\\bin\\
verbose=true
debug=true
logdir=C:\\Program Files\\Cloudbase Solutions\\Cloudbase-Init\\log\\
logfile=$2
default_log_levels=comtypes=INFO,suds=INFO,iso8601=WARN,requests=WARN
local_scripts_path=C:\\Program Files\\Cloudbase Solutions\\Cloudbase-Init\\LocalScripts\\
metadata_services=cloudbaseinit.metadata.services.nocloudservice.NoCloudConfigDriveService
plugins=$1
allow_reboot=$3
stop_service_on_exit=false
check_latest_version=false
CONF
}
P=cloudbaseinit.plugins
cloudbase_conf "$P.common.mtu.MTUPlugin,$P.common.sethostname.SetHostNamePlugin,$P.windows.extendvolumes.ExtendVolumesPlugin" \
  cloudbase-init-unattend.log false > "$WORK/cd/cloudbase-init-unattend.conf"
cloudbase_conf "$P.common.mtu.MTUPlugin,$P.common.sethostname.SetHostNamePlugin,$P.common.networkconfig.NetworkConfigPlugin,$P.windows.extendvolumes.ExtendVolumesPlugin,$P.common.userdata.UserDataPlugin,$P.common.localscripts.LocalScriptsPlugin" \
  cloudbase-init.log true > "$WORK/cd/cloudbase-init.conf"

cat > "$WORK/cd/maburvm-setup.ps1" <<'PS1'
# Runs once, at the first sign-in after Windows is installed.
$ErrorActionPreference = 'Stop'
Start-Transcript -Path C:\Windows\Temp\maburvm-setup.log
$cd = Split-Path -Parent $MyInvocation.MyCommand.Path
$cb = 'C:\Program Files\Cloudbase Solutions\Cloudbase-Init'

# virtio drivers (balloon, serial, network) and the QEMU guest agent.
$tools = Get-PSDrive -PSProvider FileSystem | ForEach-Object { Join-Path $_.Root 'virtio-win-guest-tools.exe' } | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $tools) { throw 'the virtio-win disc is not attached' }
$p = Start-Process -Wait -PassThru -FilePath $tools -ArgumentList '/install', '/passive', '/norestart'
if ($p.ExitCode -notin 0, 3010) { throw "virtio guest tools exited $($p.ExitCode)" }

$p = Start-Process -Wait -PassThru msiexec -ArgumentList '/i', "`"$cd\CloudbaseInitSetup.msi`"", '/qn', '/norestart', 'RUN_SERVICE_AS_LOCAL_SYSTEM=1'
if ($p.ExitCode -notin 0, 3010) { throw "cloudbase-init setup exited $($p.ExitCode)" }
Copy-Item "$cd\cloudbase-init.conf" "$cb\conf\cloudbase-init.conf" -Force
Copy-Item "$cd\cloudbase-init-unattend.conf" "$cb\conf\cloudbase-init-unattend.conf" -Force

# Remote Desktop on, with network-level authentication, and answer ping.
Set-ItemProperty 'HKLM:\System\CurrentControlSet\Control\Terminal Server' -Name fDenyTSConnections -Value 0
Set-ItemProperty 'HKLM:\System\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp' -Name UserAuthentication -Value 1
Enable-NetFirewallRule -DisplayGroup 'Remote Desktop'
New-NetFirewallRule -DisplayName 'Allow ping (IPv4)' -Protocol ICMPv4 -IcmpType 8 -Action Allow | Out-Null
New-NetFirewallRule -DisplayName 'Allow ping (IPv6)' -Protocol ICMPv6 -IcmpType 128 -Action Allow | Out-Null

# Newer Windows setups add a recovery partition after C:, which would stop C:
# from growing to the size of the VM's disk. Recovery moves onto C: instead.
$c = Get-Partition -DriveLetter C
$after = Get-Partition -DiskNumber $c.DiskNumber | Where-Object { $_.Offset -gt $c.Offset }
if ($after) {
  reagentc /disable | Out-Null
  $after | Remove-Partition -Confirm:$false
  Resize-Partition -DriveLetter C -Size (Get-PartitionSupportedSize -DriveLetter C).SizeMax
}

# The hardware clock a VM is given runs on UTC. Windows assumes local time
# unless told otherwise, and would be wrong by its whole time-zone offset.
Set-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\TimeZoneInformation' -Name RealTimeIsUniversal -Value 1 -Type DWord

# A VM is powered off from the panel with the ACPI button, signed in or not.
Set-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System' -Name shutdownwithoutlogon -Value 1
powercfg /hibernate off

Stop-Transcript
# Generalise and power off. The build is finished when the VM stops.
& "$env:SystemRoot\System32\Sysprep\sysprep.exe" /generalize /oobe /shutdown /unattend:"$cb\conf\Unattend.xml"
PS1

xorriso -as mkisofs -quiet -J -r -V UNATTEND -o "$WORK/unattend.iso" "$WORK/cd"

DISK="$WORK/disk.qcow2"
rm -f "$DISK"
qemu-img create -q -f qcow2 "$DISK" 32G

echo "Installing Windows Server $VERSION ($EDITION$([ "$CORE" = 1 ] && echo ', core')). This takes about fifteen minutes."
echo "To watch: connect a VNC viewer to 127.0.0.1:5977 on this host (tunnel it over SSH)."
# No network on purpose: nothing is fetched from inside, and an unpatched
# installer is not something to put on a network. The card is there so its
# driver is installed.
timeout 120m qemu-system-x86_64 -name maburvm-windows-build -enable-kvm -machine q35 \
  -cpu host,hv_relaxed,hv_vapic,hv_spinlocks=0x1fff,hv_time -smp 4 -m 6144 \
  -drive "file=$DISK,if=virtio,format=qcow2,cache=unsafe,discard=unmap" \
  -drive "file=$WORK/windows.iso,media=cdrom,readonly=on" \
  -drive "file=$WORK/virtio-win.iso,media=cdrom,readonly=on" \
  -drive "file=$WORK/unattend.iso,media=cdrom,readonly=on" \
  -device virtio-net-pci,netdev=n0 -netdev user,id=n0,restrict=on \
  -device virtio-serial-pci -chardev null,id=ga -device virtserialport,chardev=ga,name=org.qemu.guest_agent.0 \
  -device qemu-xhci -device usb-tablet -vga std -display none -vnc 127.0.0.1:77 \
  -qmp "unix:$WORK/qmp.sock,server,nowait" -monitor none -serial none \
  || { echo "The installer did not finish (exit $?). Run again with --keep and watch it over VNC." >&2; exit 1; }

echo "Compressing the image"
qemu-img convert -c -O qcow2 "$DISK" "$OUT.part"
mv "$OUT.part" "$OUT"
echo "Done: $OUT ($(du -h "$OUT" | cut -f1))"
echo "Add it in the panel under Templates, Add from URL: system type Windows, and this path"
echo "(the file must be at the same path on every node, or served from a URL the nodes can reach)."
