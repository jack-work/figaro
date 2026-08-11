# install.ps1: put a figaro binary on this machine and nothing else.
#
#   irm https://figar.org/install.ps1 | iex
#
# With arguments, PowerShell needs the script wrapped in a block, because a
# piped `iex` has no way to pass parameters through:
#
#   & ([scriptblock]::Create((irm https://figar.org/install.ps1))) -Version v0.24.0
#   & ([scriptblock]::Create((irm https://figar.org/install.ps1))) -Dir C:\tools\figaro
#
# Or set $env:FIGARO_VERSION / $env:FIGARO_INSTALL_DIR first, which works in
# the plain piped form.
#
# It downloads one release zip, checks its sha256 against the release's
# checksums.txt, and puts a single static binary on disk. The first-party
# skills are embedded in the binary, so one file IS the install. `fig` is the
# second name figaro answers to.
#
# Nothing here assumes figar.org served it: the raw GitHub URL is an equally
# supported source, and the script never reads a file from the repository.

[CmdletBinding()]
param(
    [string]$Version = $env:FIGARO_VERSION,
    [string]$Dir     = $env:FIGARO_INSTALL_DIR,
    [switch]$Help
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Repo      = 'jack-work/figaro'
# Asset root. Override for a mirror, or to test against a local
# `goreleaser build --snapshot` tree. Layout is always <base>/<tag>/<file>.
$BaseUrl   = if ($env:FIGARO_BASE_URL) { $env:FIGARO_BASE_URL } else { "https://github.com/$Repo/releases/download" }
$LatestUrl = "https://github.com/$Repo/releases/latest"

function Die($msg)  { Write-Host "install: $msg" -ForegroundColor Red; exit 1 }
function Say($msg)  { Write-Host "==> $msg" -ForegroundColor Cyan }
function Warn($msg) { Write-Host "install: warning: $msg" -ForegroundColor Yellow }
function Note($msg) { Write-Host "   $msg" -ForegroundColor DarkGray }

if ($Help) {
    Write-Host @'
usage: install.ps1 [-Version <vX.Y.Z>] [-Dir <path>]

Installs figaro.exe (and the fig.exe alias) from a GitHub release.

  -Version <ver>  Release to install, with or without the leading v.
                  Default: whatever /releases/latest redirects to.
  -Dir <path>     Install directory.
                  Default: $env:LOCALAPPDATA\Programs\figaro\bin

Environment:
  FIGARO_VERSION       same as -Version
  FIGARO_INSTALL_DIR   same as -Dir
  FIGARO_BASE_URL      asset root, default the GitHub releases download URL
'@
    exit 2
}

# --------------------------------------------------------------- platform

if (-not $IsWindows) {
    Die 'this installer is for Windows. On Linux and macOS use install.sh.'
}

$arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
    'X64'   { 'amd64' }
    'Arm64' { 'arm64' }
    default { Die "unsupported architecture: $_ (amd64 and arm64 only)" }
}

if (-not $Dir) { $Dir = Join-Path $env:LOCALAPPDATA 'Programs\figaro\bin' }

# ---------------------------------------------------------------- version
#
# The releases/latest redirect answers "what is current" without spending
# anyone's unauthenticated GitHub API rate limit, which on a shared network
# is exhausted by other people long before you arrive.

if (-not $Version) {
    Say 'resolving the latest release'
    $handler = [System.Net.Http.HttpClientHandler]::new()
    $handler.AllowAutoRedirect = $false
    $client = [System.Net.Http.HttpClient]::new($handler)
    try {
        $resp = $client.GetAsync($LatestUrl, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
        $loc  = $resp.Headers.Location
    } catch {
        Die "could not reach $LatestUrl ($($_.Exception.Message)). Pass -Version v0.24.0."
    } finally {
        $client.Dispose()
    }
    if (-not $loc -or $loc.ToString() -notmatch '/releases/tag/(.+)$') {
        Die "the latest-release redirect gave no tag. Pass -Version v0.24.0."
    }
    $Version = $Matches[1]
}

# The tag carries a v; the version stamped into the binary and the archive
# name does not. Accept either spelling and derive both.
$tag    = 'v' + ($Version -replace '^v', '')
$semver = $Version -replace '^v', ''

$archive    = "figaro_${semver}_windows_${arch}.zip"
$archiveUrl = "$BaseUrl/$tag/$archive"
$sumsUrl    = "$BaseUrl/$tag/checksums.txt"

Say "figaro $tag (windows/$arch) -> $Dir"

# --------------------------------------------------------------- download

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("figaro-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
try {
    $zipPath  = Join-Path $tmp $archive
    $sumsPath = Join-Path $tmp 'checksums.txt'

    Note $archiveUrl
    try { Invoke-WebRequest -Uri $archiveUrl -OutFile $zipPath -UseBasicParsing }
    catch { Die "download failed: $archiveUrl`n    Check that $tag exists and ships a windows/$arch archive." }

    Note $sumsUrl
    try { Invoke-WebRequest -Uri $sumsUrl -OutFile $sumsPath -UseBasicParsing }
    catch { Die "download failed: $sumsUrl`n    The release has no checksums.txt: refusing to install unverified." }

    $want = $null
    foreach ($line in Get-Content $sumsPath) {
        $parts = $line -split '\s+', 2
        if ($parts.Count -eq 2 -and ($parts[1].Trim().TrimStart('*')) -eq $archive) { $want = $parts[0].Trim(); break }
    }
    if (-not $want) { Die "$archive is not listed in checksums.txt: refusing to install" }

    $got = (Get-FileHash -Algorithm SHA256 -Path $zipPath).Hash.ToLower()
    if ($want.ToLower() -ne $got) {
        Die "checksum mismatch for $archive`n    expected $want`n    got      $got`n    Do not run the downloaded file."
    }
    Say 'sha256 ok'

    Expand-Archive -Path $zipPath -DestinationPath (Join-Path $tmp 'unpacked') -Force
    $src = Join-Path $tmp 'unpacked\figaro.exe'
    if (-not (Test-Path $src)) { Die "$archive contains no figaro.exe" }

    # ------------------------------------------------------------ install

    New-Item -ItemType Directory -Path $Dir -Force | Out-Null
    $dest = Join-Path $Dir 'figaro.exe'
    $fig  = Join-Path $Dir 'fig.exe'

    # Windows will not let you overwrite a running executable, but it WILL
    # let you rename one: the file object stays open under its new name and
    # the old process keeps running off it. So move the incumbent aside,
    # drop the new binary in, then try to sweep up. A leftover .old file is
    # untidy, not broken, and the next run deletes it.
    Get-ChildItem -Path $Dir -Filter 'figaro.exe.old-*' -ErrorAction SilentlyContinue |
        ForEach-Object { Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue }
    if (Test-Path $dest) {
        $stale = "$dest.old-" + [guid]::NewGuid().ToString('N').Substring(0, 8)
        try { Move-Item -Path $dest -Destination $stale -Force }
        catch { Die "cannot replace $dest ($($_.Exception.Message)).`n    Close any running figaro (figaro stop) and try again." }
    }
    Copy-Item -Path $src -Destination $dest -Force

    # A COPY, not a symlink. New-Item -ItemType SymbolicLink needs either
    # Developer Mode or an elevated shell on Windows, and an installer that
    # demands elevation for an alias is an installer people stop running.
    # figaro dispatches on the basename of argv[0], so a copy named fig.exe
    # behaves exactly like the symlink the Unix installer makes.
    Copy-Item -Path $dest -Destination $fig -Force

    # The channel marker. internal/update reads this file
    # (update.ChannelMarker, ".figaro-channel") from beside the binary to
    # learn how figaro got here, and on `script` it tells the user to re-run
    # the installer rather than guessing at an upgrade command that could
    # damage a brew, nix, or go-install copy. The recognised words are
    # script, homebrew, go-install and nix; anything else is ignored, so do
    # not invent one. Written to a temp name and renamed, like the binary:
    # never a half-written marker beside a good binary.
    $marker    = Join-Path $Dir '.figaro-channel'
    $markerTmp = "$marker.new"
    try {
        Set-Content -Path $markerTmp -Value 'script' -NoNewline -Encoding ascii
        Move-Item -Path $markerTmp -Destination $marker -Force
    } catch {
        Remove-Item $markerTmp -Force -ErrorAction SilentlyContinue
        Warn "could not write $marker : figaro update will not know how you installed"
    }

    # ------------------------------------------------------------- report

    $installed = $null
    try { $installed = & $dest --version 2>$null | Select-Object -First 1 } catch { }
    if (-not $installed) { Die "installed $dest but it will not run. Corrupt download, or wrong architecture." }

    Say "installed $installed"
    Note $dest
    Note $fig

    # The skills are embedded in the binary, so a truncated artifact can still
    # print a version and still be useless. `doctor skills` lists what this
    # binary actually carries: the cheapest proof it is whole. A release older
    # than the command cannot answer, so this warns rather than refuses.
    $ok = $true
    try { & $dest doctor skills *> $null; $ok = ($LASTEXITCODE -eq 0) } catch { $ok = $false }
    if (-not $ok) {
        Warn "``figaro doctor skills`` did not succeed. Either this release predates the command, or the binary is incomplete. Check by hand: $dest doctor skills"
    }

    # The user PATH is persistent; $env:Path is this session only. Set both,
    # or the install "works" and the next command still says figaro is not
    # recognized.
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $onPath = $userPath -and (($userPath -split ';') | Where-Object { $_.TrimEnd('\') -ieq $Dir.TrimEnd('\') })
    if (-not $onPath) {
        $newPath = if ([string]::IsNullOrEmpty($userPath)) { $Dir } else { "$userPath;$Dir" }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        $env:Path = "$env:Path;$Dir"
        Warn "added $Dir to your user PATH. Open a new terminal for it to take effect elsewhere."
    }

    # A daemon started by the OLD binary keeps serving until told to stop:
    # a new file on disk means nothing to a process already running.
    $running = Get-Process -Name figaro -ErrorAction SilentlyContinue
    if ($running) {
        Warn 'a figaro daemon is already running. It is still the old binary. Run:  figaro stop'
    }

    Write-Host ''
    Write-Host 'figaro is installed.' -ForegroundColor Green -NoNewline
    Write-Host ' Also reachable as fig.'
    Write-Host ''
    Write-Host '  figaro login anthropic      # or: figaro login copilot'
    Write-Host '  figaro -- buongiorno'
}
finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
