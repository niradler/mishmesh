param([string] $Binary = 'mishmesh-server.exe')
$ErrorActionPreference = 'Stop'
if ($Binary -notmatch '^mishmesh-server(?:-[a-z0-9]+)?\.exe$') {
    throw 'Choose a named Mishmesh validation executable'
}
$fixtureRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../../../output/browser-real'))
$fixtureBinary = [System.IO.Path]::GetFullPath((Join-Path $fixtureRoot $Binary))
if ([System.IO.Path]::GetDirectoryName($fixtureBinary) -ne $fixtureRoot) {
    throw 'Fixture executable must stay inside output/browser-real'
}
$fixtureProcesses = @(Get-CimInstance Win32_Process -Filter "Name = '$Binary'" | Where-Object { $_.ExecutablePath -eq $fixtureBinary })
if ($fixtureProcesses.Count -ne 1) {
    throw "Expected one owned fixture process, found $($fixtureProcesses.Count)"
}
Write-Host "Stopping owned validation process $($fixtureProcesses[0].ProcessId), database/key retained"
Stop-Process -Id $fixtureProcesses[0].ProcessId
