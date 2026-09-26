# 探针 Tanzhen · Windows 一键扎针 (PowerShell)
# 用法:
#   irm 'http://HUB/install.ps1?hub=http://HUB&token=TOKEN' | iex
#   $env:TANZHEN_HUB='http://HUB'; $env:TANZHEN_TOKEN='TOKEN'; irm 'http://HUB/install.ps1' | iex
#
# The hub injects the same two $env: lines ahead of this body when the
# query-string form is used. Plain variables rather than a param() block: iex
# evaluates this as a scriptblock, where param() must be the first statement and
# would collide with the injected lines.

$ErrorActionPreference = 'Stop'

# PowerShell 5.1 still negotiates TLS 1.0 by default, which most CDNs refuse.
# Pin the modern protocols before the first request.
try {
    $tls = [Net.SecurityProtocolType]::Tls12 -bor [Net.SecurityProtocolType]::Tls11 -bor [Net.SecurityProtocolType]::Tls
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor $tls
} catch { }

$HubUrl     = if ($env:TANZHEN_HUB)     { $env:TANZHEN_HUB.TrimEnd('/') }     else { '' }
$Token      = if ($env:TANZHEN_TOKEN)   { $env:TANZHEN_TOKEN }                else { '' }
$InstallDir = if ($env:TANZHEN_DIR)     { $env:TANZHEN_DIR }                  elseif ($env:ProgramFiles) { Join-Path $env:ProgramFiles 'Tanzhen' } else { 'C:\Program Files\Tanzhen' }
$Interval   = if ($env:TANZHEN_INTERVAL) { $env:TANZHEN_INTERVAL }            else { '2s' }
$ProbeEvery = if ($env:TANZHEN_PROBE_INTERVAL) { $env:TANZHEN_PROBE_INTERVAL } elseif ($env:TANZHEN_PROBE_EVERY) { $env:TANZHEN_PROBE_EVERY } else { '30s' }
$ProbeCount = if ($env:TANZHEN_PROBE_COUNT) { [int]$env:TANZHEN_PROBE_COUNT } else { 4 }
$ProbeProvinces = if ($env:TANZHEN_PROBE_PROVINCES) { $env:TANZHEN_PROBE_PROVINCES } else { '' }
$ProbeDisable = $env:TANZHEN_PROBE_DISABLE -in @('1','true','TRUE','yes','YES','on','ON')
$NoService  = $env:TANZHEN_NO_SERVICE -eq '1'

function Get-AgentArch {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        'x86'   { return '386' }
        default { throw "不支持的架构: $env:PROCESSOR_ARCHITECTURE" }
    }
}

function Save-SecretFile {
    param([string]$Path, [string]$Value)
    # Write, then harden: only SYSTEM and Administrators may read the token.
    Set-Content -Path $Path -Value $Value -Encoding ASCII -NoNewline
    $acl = Get-Acl -Path $Path
    $acl.SetAccessRuleProtection($true, $false)   # drop inherited rules
    foreach ($sid in 'S-1-5-18', 'S-1-5-32-544') {  # SYSTEM, BUILTIN\Administrators
        $acl.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule(
            $sid, 'Read', 'Allow')))
    }
    Set-Acl -Path $Path -AclObject $acl
}

function Install-Tanzhen {
    param(
        [Parameter(Mandatory = $true)][string]$HubUrl,
        [Parameter(Mandatory = $true)][string]$Token,
        [string]$Dir,
        [string]$Interval,
        [string]$ProbeEvery,
        [int]$ProbeCount,
        [string]$ProbeProvinces,
        [switch]$ProbeDisable,
        [switch]$NoService
    )

    $HubUrl = $HubUrl.TrimEnd('/')
    $arch = Get-AgentArch
    New-Item -ItemType Directory -Force -Path $Dir | Out-Null
    $dest = Join-Path $Dir 'tanzhen-agent.exe'
    $tokenFile = Join-Path $Dir 'token'

    $url = "$HubUrl/releases/tanzhen-agent-windows-$arch.exe"
    Write-Host "下载 agent: $url"
    $ok = $false
    try {
        Invoke-WebRequest -Uri $url -OutFile $dest -UseBasicParsing -ErrorAction Stop
        $ok = $true
    } catch {
        Write-Warning "Hub 未提供二进制，尝试 GitHub Releases"
    }
    if (-not $ok) {
        $gh = "https://github.com/FengBujue0104/tanzhen/releases/latest/download/tanzhen-agent-windows-$arch.exe"
        Invoke-WebRequest -Uri $gh -OutFile $dest -UseBasicParsing
    }

    Save-SecretFile -Path $tokenFile -Value $Token

    # The token is passed as a file, never as an argument: command lines are
    # visible to every process on the box through the task list.
    $agentArgs = @('--hub', $HubUrl, '--token-file', $tokenFile,
                   '--interval', $Interval, '--probe-every', $ProbeEvery,
                   '--probe-count', "$ProbeCount")
    if ($ProbeProvinces) { $agentArgs += @('--probe-provinces', $ProbeProvinces) }
    if ($ProbeDisable) { $agentArgs += @('--probe-disable') }

    if (-not $NoService) {
        $svc = 'TanzhenAgent'
        if (Get-ScheduledTask -TaskName $svc -ErrorAction SilentlyContinue) {
            Stop-Process -Name 'tanzhen-agent' -Force -ErrorAction SilentlyContinue
            Unregister-ScheduledTask -TaskName $svc -Confirm:$false
            Start-Sleep -Seconds 1
        }
        $action    = New-ScheduledTaskAction -Execute $dest -Argument ($agentArgs -join ' ')
        $trigger   = New-ScheduledTaskTrigger -AtStartup
        $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
        $settings  = New-ScheduledTaskSettingsSet -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
        Register-ScheduledTask -TaskName $svc -Action $action -Trigger $trigger `
            -Principal $principal -Settings $settings -Force | Out-Null
        Start-ScheduledTask -TaskName $svc
        Write-Host "完成。计划任务=$svc 二进制=$dest"
        Write-Host "查询状态: Get-ScheduledTask -TaskName $svc"
    } else {
        Start-Process -FilePath $dest -ArgumentList $agentArgs -WindowStyle Hidden
        Write-Host "完成（未注册服务，仅本次运行）。二进制=$dest"
    }
}

Install-Tanzhen -HubUrl $HubUrl -Token $Token -Dir $InstallDir `
    -Interval $Interval -ProbeEvery $ProbeEvery -ProbeCount $ProbeCount `
    -ProbeProvinces $ProbeProvinces -ProbeDisable:$ProbeDisable -NoService:$NoService
