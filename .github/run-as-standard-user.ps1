# Run a command as a local account that is not an administrator.
#
# GitHub's Windows runners run as a local administrator, and that is not
# a neutral detail for this suite. `permtest.DenyRead` puts a DENY entry
# for the calling user on a file and the caller then asserts it cannot
# read it; on the runner the read succeeded and
# TestFileManagedRefusesAnUnreadableFile failed saying the file was
# "already in the requested state" -- reporting the code under test for
# a condition the environment never created.
#
# It is the same failure the unix side has with root and
# CAP_DAC_OVERRIDE, which internal/fileperm/permtest handles by skipping
# and saying why. Skipping is the honest answer when the environment
# cannot be changed. Here it can be: a standard account keeps the
# coverage instead of trading it away, and it is also the account a hub
# actually runs as.
#
# Everything the suite touches has to be reachable by that account,
# which is what most of this script is.
[CmdletBinding()]
param(
    # The program to run, and its arguments. Passed through verbatim.
    [Parameter(Mandatory = $true, ValueFromRemainingArguments = $true)]
    [string[]]$Command
)

$ErrorActionPreference = 'Stop'
$user = 'halite-ci'

# A password nobody needs to know: it exists because
# CreateProcessWithLogonW wants credentials, and it lives for the length
# of one job on a throwaway machine.
Add-Type -AssemblyName 'System.Web'
$plain = [System.Web.Security.Membership]::GeneratePassword(24, 6)
$secure = ConvertTo-SecureString $plain -AsPlainText -Force

if (Get-LocalUser -Name $user -ErrorAction SilentlyContinue) {
    Set-LocalUser -Name $user -Password $secure
} else {
    # Deliberately not added to any group beyond the default Users. The
    # whole point is an account whose token does not carry
    # Administrators, because that is what makes a DENY entry for this
    # user actually deny.
    New-LocalUser -Name $user -Password $secure -AccountNeverExpires `
        -PasswordNeverExpires -UserMayNotChangePassword | Out-Null
}

$work = Join-Path $env:RUNNER_TEMP 'halite-ci'
New-Item -ItemType Directory -Force -Path $work, "$work\cache", "$work\mod", "$work\tmp" | Out-Null

# The workspace has to be writable: `go test` writes nothing into it,
# but the audits shell out to `go run ./tools/gendocs` and the toolchain
# wants somewhere for its own scratch.
foreach ($dir in @($PWD.Path, $work)) {
    icacls $dir /grant "${user}:(OI)(CI)M" /T /Q | Out-Null
}

# Go itself, wherever setup-go put it. Resolved rather than assumed,
# because the hosted tool cache path is not a contract.
$go = (Get-Command go).Source
icacls (Split-Path -Parent (Split-Path -Parent $go)) /grant "${user}:(OI)(CI)RX" /T /Q | Out-Null

# A batch file rather than a long command line: Start-Process with
# -Credential starts a fresh logon that inherits none of this session's
# environment, so the variables have to be set on the other side, and
# quoting them through -ArgumentList is how mistakes get made.
$out = Join-Path $work 'out.txt'
$script = Join-Path $work 'run.cmd'
@"
@echo off
set GOCACHE=$work\cache
set GOMODCACHE=$work\mod
set GOTMPDIR=$work\tmp
set TMP=$work\tmp
set TEMP=$work\tmp
set GOTOOLCHAIN=$env:GOTOOLCHAIN
set CGO_ENABLED=$env:CGO_ENABLED
"$go" $($Command -join ' ') > "$out" 2>&1
exit /b %ERRORLEVEL%
"@ | Set-Content -Path $script -Encoding ASCII

icacls $script /grant "${user}:RX" /Q | Out-Null

Write-Host "running as $user (not an administrator): go $($Command -join ' ')"
$cred = New-Object System.Management.Automation.PSCredential($user, $secure)
$proc = Start-Process -FilePath $script -Credential $cred `
    -WorkingDirectory $PWD.Path -Wait -PassThru

# The output was redirected inside the batch file, because
# Start-Process -Credential cannot also take -NoNewWindow and its own
# redirection writes as the *calling* user, which the new logon cannot
# reach. Printed here so the job log reads like every other job's.
if (Test-Path $out) { Get-Content $out }

if ($proc.ExitCode -ne 0) {
    Write-Host "::error::the suite failed as a standard user (exit $($proc.ExitCode))"
    exit $proc.ExitCode
}
