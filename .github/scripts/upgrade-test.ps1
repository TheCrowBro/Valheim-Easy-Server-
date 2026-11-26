<#
Upgrade and rollback against a real release, on Windows (docs/dev/multigame
spec, 7.3 steps 1-5). Installed copies update themselves from GitHub
releases, and go back by themselves when a new build doesn't start, while
Valheim keeps running. So before anything is released, CI checks with a
stand-in Valheim server running the whole time that:

  1-2. the newest real release (or the build-N tag given) starts on a new
       data folder, installs the stand-in server, starts it and posts in a
       stand-in Discord;
  3-4. it updates itself to this commit (copy N), which takes the running
       server over without restarting it or posting again and watches it
       for three minutes; N then updates itself to copy N+1 the same way,
       which proves a build can deliver its own fix; and an update to a
       build that doesn't start (whoami.exe) goes back to N+1 by itself,
       still with the same server;
  5.   the old release, started again on the same folder, takes the server
       back, keeps the settings and its own build number, then stops the
       server (saving the world), starts it and stops it again.

The numbered copies are built here, into a test folder that is never
uploaded, and they only ever talk to the stand-in GitHub (cmd/ci-stubs), so a
test copy can never update itself to a real release in the middle of the
test.

Usage: upgrade-test.ps1 [-From build-N]    (default: the newest build-N release)
Needs go and gh (with GH_TOKEN), and GITHUB_REPOSITORY and RUN_NUMBER as
GitHub Actions sets them. Run it after the icon and version details and the
waker are made, so the copies carry them as a release does.
#>
param(
	[string]$From = ""
)
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

if (-not $env:GITHUB_REPOSITORY -or -not $env:RUN_NUMBER) { throw "Set GITHUB_REPOSITORY and RUN_NUMBER as GitHub Actions does." }
$repoDir = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$tempBase = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
$root = Join-Path $tempBase "ves-upgrade"
$data = Join-Path $root "data"
$release = Join-Path $root "release"
$app = Join-Path $root "app\ValheimEasyServer.exe"
$fake = Join-Path $root "bin\fakevalheim.exe"
$stubsExe = Join-Path $root "bin\ci-stubs.exe"
$panel = "http://127.0.0.1:18457"
$stubGitHub = "http://127.0.0.1:18500"
$stubDiscord = "http://127.0.0.1:18501"
# A made-up webhook: with --dev-fake-discord the app sends its posts to the
# stand-in instead of discord.com.
$webhook = "https://discord.com/api/webhooks/100000000000000001/ci-upgrade-test"
$script:st = $null # the last status Wait-For saw
$stubs = $null

function Fail([string]$msg) {
	Write-Host "::error::$msg"
	throw $msg
}

function Step([string]$msg) {
	Write-Host ""
	Write-Host "=== $msg"
}

function Summary($s) {
	$s | Select-Object state, installed, app, job, discord, lastExit, live, sleep | ConvertTo-Json -Depth 6 -Compress
}

# Get-Status is the app's status, or $null while it doesn't answer (it
# doesn't for a few seconds while it restarts into another build).
function Get-Status {
	try { Invoke-RestMethod "$panel/api/status" -TimeoutSec 5 } catch { $null }
}

# Wait-For waits until the status passes $cond, and keeps that status in
# $script:st.
function Wait-For([int]$seconds, [string]$what, [scriptblock]$cond) {
	$deadline = (Get-Date).AddSeconds($seconds)
	while ((Get-Date) -lt $deadline) {
		$s = Get-Status
		if ($s -and (& $cond $s)) {
			$script:st = $s
			return
		}
		Start-Sleep -Seconds 2
	}
	$last = Get-Status
	if ($last) { Write-Host "The last status: $(Summary $last)" }
	Fail "Timed out after $seconds seconds waiting for $what."
}

# Expect checks the last status.
function Expect([scriptblock]$cond, [string]$why) {
	if (-not (& $cond $script:st)) { Fail "$why (status: $(Summary $script:st))" }
}

function Get-Settings {
	(Invoke-RestMethod "$panel/api/settings" -TimeoutSec 30).settings
}

# Api calls the control panel's API as the panel does. A refusal fails the
# test with the app's reason.
function Api([string]$method, [string]$path, [string]$body = "{}") {
	try {
		Invoke-RestMethod -Method $method -Uri "$panel$path" -Headers @{ "X-VES" = "1" } -ContentType "application/json; charset=utf-8" -Body $body -TimeoutSec 60
	} catch {
		Fail "$method $path was refused: $($_.ErrorDetails.Message) $($_.Exception.Message)"
	}
}

# Update asks the app to install the newest build it finds now. A refusal
# is shown, and the wait that follows says what didn't happen.
function Update {
	try {
		Invoke-RestMethod -Method Post -Uri "$panel/api/app/update" -Headers @{ "X-VES" = "1" } -ContentType "application/json; charset=utf-8" -Body '{"install":true}' -TimeoutSec 60 | Out-Null
	} catch {
		Write-Host "The update request was refused: $($_.ErrorDetails.Message) $($_.Exception.Message)"
	}
}

# Stub tells the stand-in GitHub what to offer.
function Stub([string]$path, $body) {
	try {
		Invoke-RestMethod -Method Post -Uri "$stubGitHub$path" -ContentType "application/json; charset=utf-8" -Body ($body | ConvertTo-Json -Compress) -TimeoutSec 60 | Out-Null
	} catch {
		Fail "The stand-in GitHub refused ${path}: $($_.ErrorDetails.Message) $($_.Exception.Message)"
	}
}

function Publish([int]$build, [string]$file) {
	Stub "/ci/publish" @{ build = $build; file = $file }
	Write-Host "The stand-in GitHub now offers $file as build-$build."
}

function Get-Posts {
	$p = @(Invoke-RestMethod "$stubDiscord/ci/posts" -TimeoutSec 5)
	return , $p
}

function Show-Posts {
	ConvertTo-Json -InputObject @((Get-Posts) | ForEach-Object { $_.content }) -Compress
}

# Wait-Posts waits until Discord has had $count posts and the last one says
# $text.
function Wait-Posts([int]$count, [string]$text) {
	$deadline = (Get-Date).AddSeconds(60)
	while ((Get-Date) -lt $deadline) {
		$p = Get-Posts
		if ($p.Count -eq $count -and $p[$count - 1].content.Contains($text)) { return }
		Start-Sleep -Seconds 1
	}
	Fail "Expected Discord post $count to say `"$text`"; the posts are: $(Show-Posts)"
}

# Read-Json reads one of the app's files without getting in its way: the
# app replaces them by renaming a new file over the old one, which Windows
# refuses while someone has the old one open without sharing delete.
function Read-Json([string]$name) {
	$f = [IO.File]::Open((Join-Path $data $name), [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]"ReadWrite, Delete")
	try {
		([IO.StreamReader]::new($f)).ReadToEnd() | ConvertFrom-Json
	} finally {
		$f.Dispose()
	}
}

function Get-SettingsSnapshot {
	$s = Read-Json "settings.json"
	"password=$($s.password) world=$($s.world) port=$($s.port)"
}

# Test-Server: the server process started in step 2 still runs (the same
# PID and start time, so a reused PID doesn't count).
function Test-Server {
	$p = Get-Process -Id $serverPid -ErrorAction SilentlyContinue
	return [bool]($p -and $p.StartTime -eq $serverStart)
}

# Get-WorldGen is the newest save generation of the world (Valheim 1.0
# writes _main.<generation>.* each time it saves), or -1.
function Get-WorldGen {
	$best = -1
	foreach ($f in @(Get-ChildItem -Path (Join-Path $data "saves\worlds_local\$world") -Filter "_main.*.fwl2" -ErrorAction SilentlyContinue)) {
		if ($f.Name -match '^_main\.(\d+)\.fwl2$' -and [int]$Matches[1] -gt $best) { $best = [int]$Matches[1] }
	}
	return $best
}

function Start-App {
	# Start-Process doesn't quote arguments with spaces itself.
	Start-Process -FilePath $app -ArgumentList "--no-window", "--data", "`"$data`"", "--port", "18457", "--dev-fake-server", "`"$fake`"",
		"--dev-fake-github", $stubGitHub, "--dev-fake-discord", $stubDiscord | Out-Null
}

function Wait-AppGone {
	$deadline = (Get-Date).AddSeconds(90)
	while (Get-Process -Name ValheimEasyServer -ErrorAction SilentlyContinue) {
		if ((Get-Date) -gt $deadline) { Fail "The app didn't close." }
		Start-Sleep -Seconds 1
	}
}

# Check-Server: the server started in step 2 is still the one running,
# online, announced once, and the settings are as they were.
function Check-Server([string]$when) {
	Expect { param($s) $s.state -eq "online" } "${when}: the server isn't online."
	$rt = Read-Json "runtime.json"
	if ($rt.pid -ne $serverPid) { Fail "${when}: runtime.json names process $($rt.pid), not the server started in step 2 ($serverPid)." }
	if (-not (Test-Server)) { Fail "${when}: the server process ($serverPid) is gone." }
	if ((Get-Posts).Count -ne 1) { Fail "${when}: Discord got these posts instead of one: $(Show-Posts)" }
	$now = Get-SettingsSnapshot
	if ($now -ne $settingsBefore) { Fail "${when}: settings.json's password, world or port changed: $now, was $settingsBefore." }
	$damaged = @(Get-ChildItem -Path $data -Recurse -Filter "*.damaged-*" -ErrorAction SilentlyContinue)
	if ($damaged.Count -gt 0) { Fail "${when}: a file was found damaged and set aside: $($damaged.FullName -join ', ')" }
	Write-Host "${when}: the same server (PID $serverPid) is online, posted once, settings unchanged."
}

function Check-Activity([string]$text) {
	Expect { param($s) @($s.activity | Where-Object { $_.text -like "$text*" }).Count -gt 0 } "The activity doesn't say: $text"
}

Set-Location $repoDir
try {
	Step "1. The release to start from"
	if (-not $From) {
		# The newest build-N, which is what installed copies update to.
		$tags = @(gh release list --repo $env:GITHUB_REPOSITORY --exclude-drafts --exclude-pre-releases --limit 100 --json tagName --jq ".[].tagName")
		if ($LASTEXITCODE -ne 0) { Fail "Couldn't list the releases." }
		$newest = $tags | Where-Object { $_ -match '^build-\d{1,9}$' } | ForEach-Object { [int]$_.Substring(6) } | Sort-Object -Descending | Select-Object -First 1
		if ($null -eq $newest) { Fail "There's no build-N release to start from." }
		$From = "build-$newest"
	}
	if ($From -match '^build-(\d{1,9})$') { $old = [int]$Matches[1] } else { Fail "Give a release tag such as build-29, not `"$From`"." }

	Remove-Item -Recurse -Force $root -ErrorAction SilentlyContinue
	foreach ($dir in "bin", "release", "builds", "app", "stubs") { New-Item -ItemType Directory -Force (Join-Path $root $dir) | Out-Null }
	gh release download $From --repo $env:GITHUB_REPOSITORY --dir $release --pattern ValheimEasyServer.exe --pattern ValheimEasyServer.exe.sha256
	if ($LASTEXITCODE -ne 0) { Fail "Couldn't download $From's program." }
	$want = ((Get-Content (Join-Path $release "ValheimEasyServer.exe.sha256") -Raw).Trim() -split '\s+')[0].ToLower()
	$got = (Get-FileHash (Join-Path $release "ValheimEasyServer.exe") -Algorithm SHA256).Hash.ToLower()
	if ($got -ne $want) { Fail "$From's program doesn't match its checksum." }
	Copy-Item (Join-Path $release "ValheimEasyServer.exe") $app

	# The copies are numbered above the release even when this run's number
	# isn't (a re-run of an older run).
	$n = [int]$env:RUN_NUMBER
	if ($n -le $old) {
		$n = $old + 1
		Write-Host "This run's number ($env:RUN_NUMBER) isn't above $From, so the test copies are numbered from $n."
	}
	$next = $n + 1
	$broken = $n + 2
	$brokenExe = Join-Path $env:SystemRoot "System32\whoami.exe" # a program, but not this app: it doesn't start as one

	Write-Host "Starting from $From; this commit is tested as builds $n and $next."
	go build -o $stubsExe ./cmd/ci-stubs
	if ($LASTEXITCODE -ne 0) { Fail "Couldn't build cmd/ci-stubs." }
	go build -o $fake ./cmd/fakevalheim
	if ($LASTEXITCODE -ne 0) { Fail "Couldn't build cmd/fakevalheim." }
	foreach ($b in $n, $next) {
		$out = Join-Path $root "builds\build-$b\ValheimEasyServer.exe"
		New-Item -ItemType Directory -Force (Split-Path $out) | Out-Null
		go build -trimpath -ldflags "-H=windowsgui -s -w -X main.version=build-$b -X main.build=$b" -o $out .
		if ($LASTEXITCODE -ne 0) { Fail "Couldn't build this commit as build $b." }
	}

	$stubs = Start-Process -FilePath $stubsExe -ArgumentList "-dir", "`"$(Join-Path $root 'stubs')`"" -PassThru -NoNewWindow `
		-RedirectStandardError (Join-Path $root "ci-stubs.log")
	$deadline = (Get-Date).AddSeconds(30)
	while ($true) {
		$ready = try { Invoke-RestMethod "$stubGitHub/ci/ready" -TimeoutSec 5 | Out-Null; Invoke-RestMethod "$stubDiscord/ci/ready" -TimeoutSec 5 | Out-Null; $true } catch { $false }
		if ($ready) { break }
		if ($stubs.HasExited -or (Get-Date) -gt $deadline) { Fail "The stand-ins for GitHub and Discord didn't start." }
		Start-Sleep -Milliseconds 500
	}

	Step "2. $From installs the stand-in server, starts it and posts in Discord"
	Start-App
	Wait-For 120 "$From to start" { param($s) $s.app.build -eq $old }
	$settings = Get-Settings
	$settings | Add-Member -NotePropertyName discord -NotePropertyValue $true -Force
	$settings | Add-Member -NotePropertyName discordWebhook -NotePropertyValue $webhook -Force
	Api POST /api/settings ($settings | ConvertTo-Json -Depth 20 -Compress) | Out-Null
	Api POST /api/server/install '{"repair":false}' | Out-Null
	Wait-For 180 "the stand-in server to be installed" { param($s) $s.installed -and ($null -eq $s.job -or $null -ne $s.job.finished) }
	Expect { param($s) -not $s.job.error } "Installing failed."
	Api POST /api/server/start | Out-Null
	Wait-For 180 "the server to be online and posted in Discord" { param($s) $s.state -eq "online" -and $s.join.code -and $null -ne $s.discord.posted }
	Expect { param($s) -not $s.discord.error } "Posting in Discord failed."
	Wait-Posts 1 " is online"
	$serverPid = [int](Read-Json "runtime.json").pid
	$serverStart = (Get-Process -Id $serverPid).StartTime
	$world = (Read-Json "settings.json").world
	$settingsBefore = Get-SettingsSnapshot
	Write-Host "The server (PID $serverPid) is online with the world $world, and Discord got the join code."

	Step "3. $From updates itself to this commit, build $n"
	Publish $n (Join-Path $root "builds\build-$n\ValheimEasyServer.exe")
	Update
	Wait-For 180 "the update to build $n" { param($s) $s.app.build -eq $n -and $s.state -eq "online" }

	Step "4. Build $n took the server over"
	Check-Server "After the update to build $n"
	Check-Activity "The app was updated to build $n"
	$since = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
	Write-Host "Keeping build $n running for three minutes with the server it took over..."
	$end = (Get-Date).AddMinutes(3)
	while ((Get-Date) -lt $end) {
		Start-Sleep -Seconds 15
		if (-not (Test-Server)) { Fail "The server process ($serverPid) ended while build $n watched it." }
		Wait-For 30 "build $n to answer" { param($s) $s.app.build -eq $n -and $s.state -eq "online" }
	}
	Expect { param($s) $s.live.t -gt $since -and $s.live.running -and $s.live.mem -gt 0 } "Build $n hasn't measured the server it took over since then."
	Expect { param($s) $s.sleep.available } "Build $n's setup checks haven't run."
	Expect { param($s) $null -ne $s.app.checkedAt -and $null -eq $s.app.latest -and -not $s.app.error } `
		"Build $n's own update check didn't run two minutes in, or found something to update to."
	Check-Server "After three minutes on build $n"

	Publish $next (Join-Path $root "builds\build-$next\ValheimEasyServer.exe")
	Update
	Wait-For 180 "build $n to update itself to build $next" { param($s) $s.app.build -eq $next -and $s.state -eq "online" }
	Check-Server "After the update to build $next"
	Check-Activity "The app was updated to build $next"

	Write-Host "An update to a build that doesn't start ($brokenExe as build $broken):"
	Publish $broken $brokenExe
	Update
	Wait-For 240 "the app to go back to build $next" { param($s) $s.app.build -eq $next -and $s.app.failed -eq $broken -and $s.state -eq "online" }
	Check-Server "After build $broken didn't start"
	Check-Activity "Build $broken of the app didn't start"

	Step "5. Back to $From on the same folder"
	# Nothing newer stays on offer, and automatic updates are off, so the old
	# release can't update itself again two minutes in.
	Stub "/ci/unpublish" @{ above = $old }
	$settings = Get-Settings
	$settings.autoUpdate = $false
	Api POST /api/settings ($settings | ConvertTo-Json -Depth 20 -Compress) | Out-Null
	Api POST /api/system/quit '{"stopServer":false}' | Out-Null
	Wait-AppGone
	if (-not (Test-Server)) { Fail "Quitting the app (leaving the server running) stopped the server." }
	# The release's program goes back in place, as a rollback would put it.
	# An antivirus may still hold the file for a moment.
	for ($try = 1; ; $try++) {
		try { Copy-Item (Join-Path $release "ValheimEasyServer.exe") $app -Force; break } catch { if ($try -ge 10) { throw } }
		Start-Sleep -Seconds 1
	}
	Start-App
	Wait-For 120 "$From to start again" { param($s) $s.app.build -eq $old -and $s.state -eq "online" }
	Check-Server "Back on $From"

	$gen = Get-WorldGen
	Api POST /api/server/stop | Out-Null
	Wait-For 120 "$From to stop the server" { param($s) $s.state -eq "stopped" }
	Expect { param($s) $s.lastExit.expected -and $s.app.build -eq $old } "$From didn't stop the server cleanly."
	if ((Get-WorldGen) -le $gen) { Fail "Stopping the server didn't save the world (its newest save is still generation $gen)." }
	if (Test-Server) { Fail "The server process ($serverPid) still runs after the stop." }
	Wait-Posts 2 "has stopped"
	Write-Host "$From stopped the server it took over, and the world was saved."

	Api POST /api/server/start | Out-Null
	Wait-For 180 "$From to start the server again" { param($s) $s.state -eq "online" -and $s.app.build -eq $old -and $null -ne $s.discord.posted }
	Wait-Posts 3 " is online"
	$gen = Get-WorldGen
	Api POST /api/server/stop | Out-Null
	Wait-For 120 "$From to stop the server again" { param($s) $s.state -eq "stopped" }
	Expect { param($s) $s.lastExit.expected -and $s.app.build -eq $old } "$From didn't stop the server cleanly the second time."
	if ((Get-WorldGen) -le $gen) { Fail "The second stop didn't save the world." }
	Wait-Posts 4 "has stopped"

	# The old release checks for updates two minutes after it starts: it must
	# find nothing to update to, and stay on its own build.
	Wait-For 200 "$From's own update check" { param($s) $null -ne $s.app.checkedAt }
	Expect { param($s) $s.app.build -eq $old -and $null -eq $s.app.latest -and -not $s.app.updating } "$From would update itself."
	Api POST /api/system/quit '{"stopServer":false}' | Out-Null
	Wait-AppGone
	Write-Host ""
	Write-Host "Upgrade and rollback between $From and this commit (builds $n and $next) all worked."
} finally {
	Write-Host ""
	Write-Host "--- the app's log"
	Get-Content (Join-Path $data "logs\app.log") -ErrorAction SilentlyContinue
	Write-Host "--- the stand-ins' log"
	Get-Content (Join-Path $root "ci-stubs.log") -ErrorAction SilentlyContinue
	Write-Host "--- the Discord posts"
	Get-Content (Join-Path $root "stubs\posts.json") -ErrorAction SilentlyContinue
	Get-Process ValheimEasyServer -ErrorAction SilentlyContinue | Stop-Process -Force
	Get-Process valheim_server -ErrorAction SilentlyContinue | Stop-Process -Force
	if ($stubs) { Stop-Process -Id $stubs.Id -Force -ErrorAction SilentlyContinue }
}
