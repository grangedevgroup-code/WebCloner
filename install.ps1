param([switch]$Uninstall)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repo = 'grangedevgroup-code/WebCloner'
$base = if ($env:WEBCLONER_BASE_URL) { $env:WEBCLONER_BASE_URL } else { "https://github.com/$repo/releases/latest/download" }
$dir = Join-Path $env:LOCALAPPDATA 'Programs\WebCloner'
$shortcut = Join-Path ([Environment]::GetFolderPath('Programs')) 'WebCloner.lnk'
$uninstallKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\WebCloner'

function Stop-WebCloner {
    Get-Process -Name 'WebCloner' -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -and $_.Path.StartsWith($dir, [StringComparison]::OrdinalIgnoreCase) } |
        Stop-Process -Force
}

if ($Uninstall -or $env:WEBCLONER_UNINSTALL) {
    Stop-WebCloner
    Remove-Item -Force -ErrorAction SilentlyContinue $shortcut
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $uninstallKey
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $dir
    Write-Host 'WebCloner removed.'
    return
}

if (-not [Environment]::Is64BitOperatingSystem) {
    throw 'WebCloner needs 64-bit Windows.'
}

[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("webcloner-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    $asset = 'WebCloner-windows-amd64.zip'
    $zip = Join-Path $tmp $asset
    $sums = Join-Path $tmp 'SHA256SUMS'
    Write-Host "Downloading $asset"
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $zip
    Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile $sums

    $line = Get-Content $sums | Where-Object { $_ -match "\s\*?$([regex]::Escape($asset))$" } | Select-Object -First 1
    if (-not $line) { throw "$asset is not listed in SHA256SUMS" }
    $want = ($line -split '\s+')[0].ToLowerInvariant()
    $got = (Get-FileHash -Algorithm SHA256 $zip).Hash.ToLowerInvariant()
    if ($want -ne $got) { throw "Checksum mismatch for $asset" }

    $unpacked = Join-Path $tmp 'app'
    Expand-Archive -Path $zip -DestinationPath $unpacked -Force

    Stop-WebCloner
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Copy-Item -Force -Path (Join-Path $unpacked '*') -Destination $dir -Recurse
    $exe = Join-Path $dir 'WebCloner.exe'
    Get-ChildItem $dir -Recurse | Unblock-File

    $shell = New-Object -ComObject WScript.Shell
    $link = $shell.CreateShortcut($shortcut)
    $link.TargetPath = $exe
    $link.WorkingDirectory = $dir
    $link.Description = 'Copy a website to a folder on your computer'
    $link.Save()

    $uninstaller = Join-Path $dir 'uninstall.ps1'
    @"
`$dir = '$dir'
Get-Process -Name 'WebCloner' -ErrorAction SilentlyContinue | Where-Object { `$_.Path -and `$_.Path.StartsWith(`$dir, [StringComparison]::OrdinalIgnoreCase) } | Stop-Process -Force
Remove-Item -Force -ErrorAction SilentlyContinue '$shortcut'
Remove-Item -Recurse -Force -ErrorAction SilentlyContinue '$uninstallKey'
Set-Location `$env:TEMP
Remove-Item -Recurse -Force -ErrorAction SilentlyContinue `$dir
"@ | Set-Content -Encoding UTF8 $uninstaller

    $version = (Get-Item $exe).VersionInfo.ProductVersion
    if (-not $version) { $version = '' }
    New-Item -Force -Path $uninstallKey | Out-Null
    $values = @{
        DisplayName     = 'WebCloner'
        DisplayIcon     = $exe
        DisplayVersion  = $version
        Publisher       = 'Grange Dev Group'
        InstallLocation = $dir
        URLInfoAbout    = "https://github.com/$repo"
        UninstallString = "powershell.exe -NoProfile -ExecutionPolicy Bypass -File `"$uninstaller`""
    }
    foreach ($k in $values.Keys) {
        New-ItemProperty -Force -Path $uninstallKey -Name $k -Value $values[$k] -PropertyType String | Out-Null
    }
    New-ItemProperty -Force -Path $uninstallKey -Name NoModify -Value 1 -PropertyType DWord | Out-Null
    New-ItemProperty -Force -Path $uninstallKey -Name NoRepair -Value 1 -PropertyType DWord | Out-Null

    Write-Host "WebCloner installed in $dir. Open it from the Start menu."
}
finally {
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $tmp
}
