# USB-Hotspot controller for UGREEN CM496 (RTL8811CU) on wired networks.
# Actions: status | start | stop
# Designed to avoid the ICS x Clash-TUN conflict: TUN is disabled before
# StartTethering and re-enabled after the hotspot is stable, with inet
# self-checks and rollback at every step. No admin rights required.
param(
    [string]$Action = 'status',
    [string]$Ssid = 'xiaomai-AP',
    [string]$Passkey = 'wifi12345'
)
$ErrorActionPreference = 'Stop'
$report = New-Object System.Collections.Generic.List[string]
function Log($m) {
    $script:report.Add($m) | Out-Null
    Write-Host $m
}

# ---------- helpers ----------
function Find-MihomoPipe {
    $p = [System.IO.Directory]::GetFiles('\\.\pipe\') |
        Where-Object { $_ -match 'verge-mihomo-production' } |
        Select-Object -First 1
    return $p
}

function Invoke-MihomoApi {
    param([string]$Method, [string]$Path, [string]$Body = '')
    $pipePath = Find-MihomoPipe
    if (-not $pipePath) { throw 'mihomo control pipe not found - is Clash Verge running?' }
    $pipeName = $pipePath -replace '^\\\\\.\\pipe\\', ''
    $client = [System.IO.Pipes.NamedPipeClientStream]::new('.', $pipeName, [System.IO.Pipes.PipeDirection]::InOut)
    $client.Connect(4000)
    $bodyBytes = [System.Text.Encoding]::UTF8.GetBytes($Body)
    $head = "$Method $Path HTTP/1.1`r`nHost: localhost`r`nAuthorization: Bearer set-your-secret`r`nContent-Type: application/json`r`nContent-Length: $($bodyBytes.Length)`r`nConnection: close`r`n`r`n"
    $headBytes = [System.Text.Encoding]::ASCII.GetBytes($head)
    $client.Write($headBytes, 0, $headBytes.Length)
    if ($bodyBytes.Length -gt 0) { $client.Write($bodyBytes, 0, $bodyBytes.Length) }
    $client.Flush()
    $ms = [System.IO.MemoryStream]::new()
    $buf = [byte[]]::new(65536)
    try {
        while ($true) {
            $n = $client.Read($buf, 0, $buf.Length)
            if ($n -le 0) { break }
            $ms.Write($buf, 0, $n)
        }
    } catch {}
    $client.Dispose()
    $text = [System.Text.Encoding]::UTF8.GetString($ms.ToArray())
    $statusLine = ($text -split "`r`n")[0]
    $bodyText = ''
    $idx = $text.IndexOf("`r`n`r`n")
    if ($idx -ge 0) {
        $headers = $text.Substring(0, $idx)
        $rest = $text.Substring($idx + 4)
        if ($headers -match '(?i)Transfer-Encoding:\s*chunked') {
            $sb = [System.Text.StringBuilder]::new()
            $pos = 0
            while ($pos -lt $rest.Length) {
                $eol = $rest.IndexOf("`r`n", $pos)
                if ($eol -lt 0) { break }
                $sizeHex = $rest.Substring($pos, $eol - $pos).Trim()
                if ($sizeHex -eq '') { $pos = $eol + 2; continue }
                $size = 0
                if (-not [System.Int32]::TryParse($sizeHex, [System.Globalization.NumberStyles]::HexNumber, $null, [ref]$size)) { break }
                if ($size -le 0) { break }
                if ($eol + 2 + $size -gt $rest.Length) { $size = $rest.Length - ($eol + 2) }
                [void]$sb.Append($rest.Substring($eol + 2, $size))
                $pos = $eol + 2 + $size + 2
            }
            $bodyText = $sb.ToString()
        } else {
            $bodyText = $rest
        }
    }
    return @{ StatusLine = $statusLine; Body = $bodyText }
}

function Get-TunEnabled {
    $r = Invoke-MihomoApi -Method 'GET' -Path '/configs'
    if ($r.StatusLine -notmatch '200') { return $null }
    try {
        $json = $r.Body | ConvertFrom-Json
        return [bool]$json.tun.enable
    } catch { return $null }
}

function Set-Tun {
    param([bool]$On)
    $body = '{"tun":{"enable":' + $(if ($On) { 'true' } else { 'false' }) + '}}'
    $r = Invoke-MihomoApi -Method 'PATCH' -Path '/configs' -Body $body
    return ($r.StatusLine -match '20[04]')
}

function Test-Inet {
    param([int]$Retries = 3, [int]$GapSec = 3)
    for ($i = 0; $i -lt $Retries; $i++) {
        try {
            $code = & curl.exe -s -o NUL -w "%{http_code}" --max-time 6 --noproxy '*' https://www.bing.com 2>$null
            if ($code -eq '200' -or $code -eq '302') { return $true }
        } catch {}
        if ($i -lt $Retries - 1) { Start-Sleep -Seconds $GapSec }
    }
    return $false
}

function Get-WlanNic {
    $n = Get-NetAdapter | Where-Object { $_.InterfaceDescription -match '8811CU' } | Select-Object -First 1
    if (-not $n) {
        # fallback: any physical wireless NIC (e.g. built-in MT7922) - WFD hotspot needs a Wi-Fi Direct capable card
        $n = Get-NetAdapter | Where-Object { $_.InterfaceDescription -match 'Wireless|Wi-?Fi' -and $_.InterfaceDescription -notmatch 'Virtual|Direct' } | Select-Object -First 1
    }
    return $n
}

function Find-UplinkProfile {
    [void][Windows.Networking.Connectivity.NetworkInformation, Windows.Networking.Connectivity, ContentType=WindowsRuntime]
    $profiles = [Windows.Networking.Connectivity.NetworkInformation]::GetConnectionProfiles()
    $eth = $null
    foreach ($p in $profiles) {
        if ($p.NetworkAdapter.NetworkAdapterId.ToString() -eq '6b5a4c6c-29b8-4067-ae1c-3933ce43e031') { $eth = $p; break }
    }
    if (-not $eth) {
        # fallback: any InternetAccess profile on a non-tunnel adapter
        foreach ($p in $profiles) {
            if ($p.GetNetworkConnectivityLevel().ToString() -eq 'InternetAccess' -and $p.ProfileName -ne 'Meta') { $eth = $p; break }
        }
    }
    return $eth
}

function Get-TetherMgr {
    [void][Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager, Windows.Networking.NetworkOperators, ContentType=WindowsRuntime]
    $nic = Get-WlanNic
    if (-not $nic) { throw 'no Wi-Fi adapter present' }
    $eth = Find-UplinkProfile
    if (-not $eth) { throw 'no internet-connected profile found to share from' }
    Log ("uplink profile: " + $eth.ProfileName)
    return [Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager]::CreateFromConnectionProfile($eth)
}

function Get-TetherStateFresh {
    # create a NEW manager instance: the old one can serve stale state
    $eth = Find-UplinkProfile
    if (-not $eth) { return 'no-uplink' }
    $m2 = [Windows.Networking.NetworkOperators.NetworkOperatorTetheringManager]::CreateFromConnectionProfile($eth)
    return $m2.TetheringOperationalState.ToString()
}

function Test-WfdAdapterUp {
    # when the hotspot is really up, its Wi-Fi Direct virtual adapter goes Up
    $wfd = Get-NetAdapter | Where-Object { $_.InterfaceDescription -match 'Microsoft Wi-?Fi Direct Virtual' -and $_.Status -eq 'Up' }
    return [bool]$wfd
}

function Wait-TetherState {
    param($Mgr, [string]$Target, [int]$Seconds)
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Milliseconds 600
        if ($Mgr.TetheringOperationalState.ToString() -eq $Target) { return $true }
    }
    return ($Mgr.TetheringOperationalState.ToString() -eq $Target)
}

function Test-Broadcast {
    param([string]$Ssid)
    $nic = Get-WlanNic
    if (-not $nic) { return $false }
    $r = & netsh wlan show networks interface="$($nic.Name)" 2>&1
    $joined = ($r | Out-String)
    return ($joined -match [regex]::Escape($Ssid))
}

# ---------- main ----------
$tunWeTurnedOff = $false
try {
    Log 'NOTE: a full run takes 60~120s - each STEP prints here as it goes'
    $nic = Get-WlanNic
    if (-not $nic) { throw 'no Wi-Fi adapter found (neither USB 8811CU nor built-in wireless card)' }
    Log ("adapter: " + $nic.Name + " [" + $nic.InterfaceDescription + "] status=" + $nic.Status)

    $mgr = Get-TetherMgr
    $state = $mgr.TetheringOperationalState.ToString()
    $cfg = $mgr.GetCurrentAccessPointConfiguration()
    Log ("hotspot: state=$state ssid=[" + $cfg.Ssid + "] clients=" + $mgr.ClientCount)

    $tun = Get-TunEnabled
    Log ("clash TUN: " + $(if ($null -eq $tun) { 'unknown (api fail)' } elseif ($tun) { 'enabled' } else { 'disabled' }))
    Log ("inet check: " + $(if (Test-Inet) { 'OK' } else { 'FAIL' }))

    if ($Action -eq 'status') {
        # nothing more
    }
    elseif ($Action -eq 'start') {
        if ($state -eq 'On') {
            Log 'hotspot already ON - skip start'
        } else {
            # STEP 1: disable TUN to avoid ICS conflict
            if ($tun) {
                Log 'STEP1 disable TUN ...'
                if (-not (Set-Tun -On $false)) { throw 'failed to disable TUN via api' }
                $tunWeTurnedOff = $true
                Start-Sleep -Seconds 6
                if (-not (Test-Inet -Retries 3 -GapSec 4)) {
                    Log 'REVERT: inet dead after TUN off - re-enabling TUN'
                    [void](Set-Tun -On $true)
                    $tunWeTurnedOff = $false
                    throw 'direct inet failed after disabling TUN - aborted (TUN restored)'
                }
                Log 'STEP1 ok: TUN off, direct inet OK'
            } else {
                Log 'STEP1 skip: TUN already off'
            }

            # STEP 2: configure ssid/pass
            Log ('STEP2 configure ssid=' + $Ssid + ' ...')
            $cfg.Ssid = $Ssid
            $cfg.Passphrase = $Passkey
            $op1 = $mgr.ConfigureAccessPointAsync($cfg)
            $d1 = (Get-Date).AddSeconds(10)
            while (((Get-Date) -lt $d1) -and ($op1.Status -eq 0)) { Start-Sleep -Milliseconds 200 }
            Log ('STEP2 result: async status=' + $op1.Status)
            $cfg2 = $mgr.GetCurrentAccessPointConfiguration()
            Log ('STEP2 verify: ssid=[' + $cfg2.Ssid + ']')

            # STEP 3: start tethering (fire, then poll state with live-signal fallback)
            Log 'STEP3 start tethering ...'
            $op2 = $mgr.StartTetheringAsync()
            $ok = Wait-TetherState -Mgr $mgr -Target 'On' -Seconds 45
            if (-not $ok) {
                Log 'STEP3 note: state still not On - cross-checking live signals ...'
                Start-Sleep -Seconds 3
                $stFresh = Get-TetherStateFresh
                $wfdUp = Test-WfdAdapterUp
                $bcNow = Test-Broadcast -Ssid $Ssid
                Log ("STEP3 crosscheck: fresh-state=$stFresh wfd-adapter-up=$wfdUp broadcast=$bcNow")
                if ($stFresh -eq 'On' -or $wfdUp -or $bcNow) {
                    $ok = $true
                    Log 'STEP3 verdict: hotspot IS running (state property lagged)'
                }
            }
            $state2 = $mgr.TetheringOperationalState.ToString()
            Log ("STEP3 result: state=$state2 " + $(if ($ok) { 'OK' } else { 'TIMEOUT/FAIL' }))
            if (-not $ok) {
                Log 'STEP3 rollback: stop tethering to clear half-open state ...'
                try { $null = $mgr.StopTetheringAsync(); Start-Sleep -Seconds 3 } catch {}
                throw 'StartTetheringAsync: no On state and no live signals within 45s'
            }

            # STEP 4: verify broadcast + inet (TUN still off)
            Start-Sleep -Seconds 3
            $bc = Test-Broadcast -Ssid $Ssid
            Log ("STEP4 broadcast visible: " + $bc)
            $inet2 = Test-Inet
            Log ("STEP4 inet (direct, TUN off): " + $(if ($inet2) { 'OK' } else { 'FAIL' }))

            # STEP 5: re-enable TUN (best effort - hotspot already works without it)
            Log 'STEP5 re-enable TUN ...'
            try {
                if (Set-Tun -On $true) {
                    $tunWeTurnedOff = $false
                    Start-Sleep -Seconds 5
                    $inet3 = Test-Inet -Retries 3 -GapSec 4
                    if ($inet3) {
                        Log 'STEP5 ok: TUN back on, inet OK'
                    } else {
                        Log 'REVERT: inet dead after TUN re-enable - disabling TUN again ...'
                        [void](Set-Tun -On $false)
                        Start-Sleep -Seconds 6
                        if (Test-Inet -Retries 3 -GapSec 4) {
                            Log 'FINAL: hotspot ON + direct net OK, TUN left OFF - re-enable via Clash GUI later'
                        } else {
                            Log 'CRITICAL: inet still dead after TUN off - reboot may be required'
                        }
                    }
                } else {
                    Log 'STEP5 warn: TUN api failed (hotspot stays ON, net on direct path)'
                }
            } catch {
                Log ('STEP5 warn: ' + $_.Exception.Message + ' (hotspot stays ON)')
            }
        }
    }
    elseif ($Action -eq 'stop') {
        if ($state -ne 'On') {
            Log 'hotspot not running - nothing to stop'
        } else {
            $op3 = $mgr.StopTetheringAsync()
            $ok = Wait-TetherState -Mgr $mgr -Target 'Off' -Seconds 20
            Log ("stop result: state=" + $mgr.TetheringOperationalState.ToString() + " " + $(if ($ok) { 'OK' } else { 'TIMEOUT' }))
        }
    }
    else {
        throw "unknown action: $Action (use status|start|stop)"
    }
} catch {
    Log ('ERROR: ' + $_.Exception.Message)
    if ($tunWeTurnedOff) {
        Log 'RECOVERY: this run disabled TUN earlier - restoring it now ...'
        try {
            if (Set-Tun -On $true) {
                Start-Sleep -Seconds 5
                if (Test-Inet -Retries 3 -GapSec 4) { Log 'RECOVERY ok: TUN on + inet OK' }
                else { Log 'RECOVERY warn: TUN on but inet check failed - check Clash GUI' }
            } else {
                Log 'RECOVERY FAILED: TUN api error - re-enable TUN in Clash GUI manually'
            }
        } catch {
            Log ('RECOVERY FAILED: ' + $_.Exception.Message + ' - re-enable TUN in Clash GUI manually')
        }
    }
    Log ('hint: run with -Action status to inspect, or -Action stop to turn hotspot off')
}
$report -join "`r`n"