# Runs one named Go test with -v and fails unless it ran and passed.
#   pwsh -NoProfile -File .taad/slices/SL-019/checks/no-skip.ps1 -Run '^TestEveryScreenIsDesigned$' -Pkg ./internal/server/
# It exits 1 when the output holds '--- SKIP' (a browser test that skipped for want of
# CHROME_BIN proved nothing) or lacks '--- PASS: <name>', and otherwise passes go
# test's own exit code through.
param(
    [Parameter(Mandatory = $true)][string]$Run,
    [Parameter(Mandatory = $true)][string]$Pkg
)
$ErrorActionPreference = 'Continue'
$name = $Run.TrimStart('^').TrimEnd('$')
$out = & go test -count=1 -v -run $Run $Pkg 2>&1 | Out-String
$code = $LASTEXITCODE
Write-Output $out
if ($out -match '--- SKIP') {
    Write-Output "no-skip: the test skipped (is CHROME_BIN set?), so it proved nothing"
    exit 1
}
if ($out -notmatch ('--- PASS: ' + [regex]::Escape($name))) {
    Write-Output "no-skip: no '--- PASS: $name' line in the output"
    exit 1
}
exit $code
