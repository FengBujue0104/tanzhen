# 探针 Tanzhen · Windows 一键安装 (PowerShell)
# 用法: irm http://HUB/install.ps1 | iex
#       Install-Tanzhen -HubUrl 'http://HUB' -Token 'TOKEN'

function Install-Tanzhen {
    param(
        [Parameter(Mandatory = $true)][string]$HubUrl,
        [Parameter(Mandatory = $true)][string]$Token,
        [string]$InstallDir = "$env:ProgramFiles\Tanzhen"
    )
    $ErrorActionPreference = "Stop"
    $HubUrl = $HubUrl.TrimEnd("/")
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $dest = Join-Path $InstallDir "tanzhen-agent.exe"

    $url = "$HubUrl/releases/tanzhen-agent-windows-amd64.exe"
    Write-Host "Downloading $url"
    try {
        Invoke-WebRequest -Uri $url -OutFile $dest -UseBasicParsing
    } catch {
        $gh = "https://github.com/FengBujue0104/tanzhen/releases/latest/download/tanzhen-agent-windows-amd64.exe"
        Write-Host "Hub miss, try GitHub $gh"
        Invoke-WebRequest -Uri $gh -OutFile $dest -UseBasicParsing
    }

    $envFile = Join-Path $InstallDir "agent.env"
    @"
HUB_URL=$HubUrl
TOKEN=$Token
"@ | Set-Content -Path $envFile -Encoding ASCII

    $svc = "TanzhenAgent"
    if (Get-Service -Name $svc -ErrorAction SilentlyContinue) {
        Stop-Service $svc -Force -ErrorAction SilentlyContinue
        sc.exe delete $svc | Out-Null
        Start-Sleep -Seconds 1
    }
    # nssm optional; fallback: scheduled task
    $action = New-ScheduledTaskAction -Execute $dest -Argument "--hub $HubUrl --token $Token"
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -LogonType ServiceAccount -RunLevel Highest
    Register-ScheduledTask -TaskName $svc -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
    Start-Process -FilePath $dest -ArgumentList "--hub",$HubUrl,"--token",$Token -WindowStyle Hidden
    Write-Host "OK. ScheduledTask=$svc binary=$dest"
}

# If invoked with args via pipeline wrapper, user calls Install-Tanzhen manually after iex.
