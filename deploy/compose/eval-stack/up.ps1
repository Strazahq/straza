# One-command eval stack with a working phone QR (Windows twin of up.sh).
# The container cannot see which of the host's addresses your phone can
# dial, so this launcher picks the default-route IPv4 and hands it to
# strazad as the advertised approver URL. Wrong pick on a multi-adapter
# machine? Override and mint again:
#   $env:STRAZA_APPROVER_TLS_PUBLIC_URL = "https://<ip>:8443"; .\up.ps1
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

if (-not $env:STRAZA_APPROVER_TLS_PUBLIC_URL) {
    # Ask the OS which source address it would route an internet-bound packet
    # from, the twin of up.sh's `ip route get`: picking the first adapter that
    # merely has a default gateway hands the QR a VPN address on any machine
    # with a VPN installed.
    $ip = $null
    try {
        $ip = (Find-NetRoute -RemoteIPAddress "1.1.1.1" -ErrorAction Stop |
            Select-Object -First 1).IPAddress
    } catch {}
    if ($ip) {
        $env:STRAZA_APPROVER_TLS_PUBLIC_URL = "https://${ip}:8443"
        Write-Host "Phone approver will advertise $env:STRAZA_APPROVER_TLS_PUBLIC_URL (default-route interface)."
        Write-Host 'For a different address, run $env:STRAZA_APPROVER_TLS_PUBLIC_URL = "https://<ip>:8443"; .\up.ps1'
    } else {
        Write-Host "Could not detect a LAN address, so a phone QR would carry an address your phone"
        Write-Host 'cannot dial. Before you add a phone, set STRAZA_APPROVER_TLS_PUBLIC_URL and re-run. Starting anyway.'
    }
} else {
    Write-Host "Phone approver will advertise $env:STRAZA_APPROVER_TLS_PUBLIC_URL (from your environment)."
}

docker compose -f compose.yaml up -d --build

Write-Host ""
Write-Host "Stack starting. Watch the seed: docker compose -p straza-eval logs -f eval-seed"
Write-Host "Start at http://localhost:8400, which lists every address, account and walkthrough."
Write-Host "Console: http://localhost:8420/console/"
Write-Host "Phone: install the Straza approver app, then console -> Approvals -> Approver devices"
Write-Host "  -> Add a phone -> scan the QR."
Write-Host "Demo defaults assume a network you trust. On any other network, use the loopback"
Write-Host "overlay chain: https://docs.straza.ai/get-started/enterprise-demo-stack/#demo-defaults-and-the-hardened-shape"
