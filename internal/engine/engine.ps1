# GZIST Hotspot engine: talks to the Windows Runtime tethering APIs, the
# one part of the hotspot that has no pure-Go bindings.
#
# Request (JSON) arrives in the HOTSPOT_REQ environment variable:
#   {"op":"discover"}
#   {"op":"configure","ssid":"...","passkey":"..."}
#   {"op":"start-try","seconds":45,"ssid":"..."}
#   {"op":"stop-wait","seconds":20}
# The answer is a single JSON object on stdout. Keep this file ASCII only
# (PowerShell 5.1 reads BOM-less scripts as ANSI); the Go side translates
# outcomes into user-facing Chinese.
$ErrorActionPreference = 'Stop'
try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false) } catch {}

[void][Windows.Networking.Connectivity.NetworkInformation, Windows.Networking.Connectivity, ContentType=WindowsRuntime]
[void][Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager, Windows.Networking.NetworkOperators, ContentType=WindowsRuntime]

function Find-WlanNic {
    # Prefer the USB card the project grew up with, else any physical
    # wireless card (a WFD hotspot needs a Wi-Fi Direct capable NIC).
    $n = Get-NetAdapter | Where-Object { $_.InterfaceDescription -match '8811CU' } | Select-Object -First 1
    if (-not $n) {
        $n = Get-NetAdapter | Where-Object { $_.InterfaceDescription -match 'Wireless|Wi-?Fi' -and $_.InterfaceDescription -notmatch 'Virtual|Direct' } | Select-Object -First 1
    }
    return $n
}

function Find-UplinkProfile {
    # A connection profile with internet access, preferring a wired one
    # (locale-independent: IsWlanConnectionProfile instead of names).
    $profiles = [Windows.Networking.Connectivity.NetworkInformation]::GetConnectionProfiles()
    $wired = $null
    $any = $null
    foreach ($p in $profiles) {
        if ($p.GetNetworkConnectivityLevel().ToString() -ne 'InternetAccess') { continue }
        if ($p.ProfileName -eq 'Meta') { continue }  # the Clash TUN adapter
        if ($null -eq $any) { $any = $p }
        $wlan = $false
        try { $wlan = [bool]$p.IsWlanConnectionProfile } catch {}
        if (-not $wlan -and $null -eq $wired) { $wired = $p }
    }
    if ($wired) { return $wired }
    return $any
}

function Get-Mgr {
    $nic = Find-WlanNic
    if (-not $nic) { throw 'no Wi-Fi adapter present' }
    $script:NicName = $nic.Name
    $script:NicDesc = $nic.InterfaceDescription
    $script:NicStatus = $nic.Status.ToString()
    $eth = Find-UplinkProfile
    if (-not $eth) { throw 'no internet-connected profile found to share from' }
    $script:Uplink = $eth.ProfileName
    return [Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager]::CreateFromConnectionProfile($eth)
}

function Test-Broadcast {
    param([string]$Ssid)
    if (-not $script:NicName) { return $false }
    try {
        $r = & netsh wlan show networks interface="$($script:NicName)" 2>$null
        return (($r | Out-String).Contains($Ssid))
    } catch { return $false }
}

$out = [ordered]@{ ok = $false }
try {
    $req = ConvertFrom-Json $env:HOTSPOT_REQ
    $out.op = [string]$req.op
    switch ($req.op) {

        'discover' {
            $mgr = Get-Mgr
            $cfg = $mgr.GetCurrentAccessPointConfiguration()
            $out.ok = $true
            $out.state = $mgr.TetheringOperationalState.ToString()
            $out.ssid = [string]$cfg.Ssid
            $out.clients = [int]$mgr.ClientCount
            $out.nic_name = [string]$script:NicName
            $out.nic_desc = [string]$script:NicDesc
            $out.nic_status = [string]$script:NicStatus
            $out.uplink = [string]$script:Uplink
        }

        'configure' {
            $mgr = Get-Mgr
            $cfg = $mgr.GetCurrentAccessPointConfiguration()
            $cfg.Ssid = [string]$req.ssid
            $cfg.Passphrase = [string]$req.passkey
            $op1 = $mgr.ConfigureAccessPointAsync($cfg)
            $deadline = (Get-Date).AddSeconds(10)
            while (((Get-Date) -lt $deadline) -and ($op1.Status -eq 0)) { Start-Sleep -Milliseconds 200 }
            $out.async_status = [int]$op1.Status
            $cfg2 = $mgr.GetCurrentAccessPointConfiguration()
            $out.ssid = [string]$cfg2.Ssid
            $out.ok = ([string]$cfg2.Ssid -eq [string]$req.ssid)
            if (-not $out.ok) { $out.error = 'ssid readback mismatch after configure' }
        }

        'start-try' {
            $mgr = Get-Mgr
            $state = $mgr.TetheringOperationalState.ToString()
            $out.state = $state
            if ($state -eq 'On') {
                $out.ok = $true
                $out.already = $true
            } else {
                # Fire, then poll: PS 5.1 cannot read IAsyncOperation results.
                [void]$mgr.StartTetheringAsync()
                $secs = 45
                if ($req.seconds) { $secs = [int]$req.seconds }
                $deadline = (Get-Date).AddSeconds($secs)
                $on = $false
                while ((Get-Date) -lt $deadline) {
                    Start-Sleep -Milliseconds 600
                    if ($mgr.TetheringOperationalState.ToString() -eq 'On') { $on = $true; break }
                }
                if (-not $on) {
                    # Cross-check live signals: the state property can lag.
                    Start-Sleep -Seconds 3
                    $fresh = 'no-uplink'
                    $eth = Find-UplinkProfile
                    if ($eth) {
                        $m2 = [Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager]::CreateFromConnectionProfile($eth)
                        $fresh = $m2.TetheringOperationalState.ToString()
                    }
                    $wfd = [bool](Get-NetAdapter | Where-Object { $_.InterfaceDescription -match 'Microsoft Wi-?Fi Direct Virtual' -and $_.Status -eq 'Up' } | Select-Object -First 1)
                    $bc = Test-Broadcast -Ssid ([string]$req.ssid)
                    $out.fresh = $fresh
                    $out.wfd = $wfd
                    $out.broadcast = $bc
                    if ($fresh -eq 'On' -or $wfd -or $bc) { $on = $true }
                }
                $out.state = $mgr.TetheringOperationalState.ToString()
                $out.ok = $on
            }
        }

        'stop-wait' {
            $mgr = Get-Mgr
            $state = $mgr.TetheringOperationalState.ToString()
            if ($state -ne 'On') {
                $out.ok = $true
                $out.nothing = $true
                $out.state = $state
            } else {
                [void]$mgr.StopTetheringAsync()
                $secs = 20
                if ($req.seconds) { $secs = [int]$req.seconds }
                $deadline = (Get-Date).AddSeconds($secs)
                while ((Get-Date) -lt $deadline) {
                    Start-Sleep -Milliseconds 600
                    if ($mgr.TetheringOperationalState.ToString() -ne 'On') { break }
                }
                $out.state = $mgr.TetheringOperationalState.ToString()
                $out.ok = ($out.state -eq 'Off')
            }
        }

        default { $out.error = 'unknown op: ' + [string]$req.op }
    }
} catch {
    $out.ok = $false
    $out.error = $_.Exception.Message
}
Write-Output ($out | ConvertTo-Json -Compress -Depth 4)
