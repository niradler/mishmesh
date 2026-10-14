param(
    [switch] $Short,
    [ValidateRange(1, 86400)] [int] $DockerDeadlineSeconds = 600,
    [string[]] $GoTestArgs = @('-race', '-count=1', '-timeout=180s', './...')
)

$ErrorActionPreference = 'Stop'
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$module = Get-Content -LiteralPath (Join-Path $projectRoot 'go.mod')
$goVersionLine = $module | Where-Object { $_ -match '^go \S+$' } | Select-Object -First 1
if (-not $goVersionLine) {
    throw 'go.mod is missing its Go version'
}
$goVersion = ($goVersionLine -split '\s+')[1]
if ($Short) {
    $GoTestArgs = @('-short', '-count=1', '-timeout=180s', './...')
}
$containerName = 'mishmesh-go-check-' + [guid]::NewGuid().ToString('N').Substring(0, 12)
$dockerArguments = @(
    'run', '--name', $containerName,
    '--mount', "type=bind,source=$projectRoot,target=/src,readonly",
    '--mount', 'type=volume,source=mishmesh-go-modules,target=/go/pkg/mod',
    '--mount', 'type=volume,source=mishmesh-go-build-cache,target=/root/.cache/go-build',
    '--workdir', '/src',
    "golang:$goVersion", 'go', 'test'
) + $GoTestArgs
Write-Host "Running Go tests in $containerName with no published ports"
$start = [System.Diagnostics.ProcessStartInfo]::new()
$start.FileName = 'docker'
$start.UseShellExecute = $false
foreach ($argument in $dockerArguments) {
    $start.ArgumentList.Add($argument)
}
$process = [System.Diagnostics.Process]::Start($start)
if (-not $process.WaitForExit($DockerDeadlineSeconds * 1000)) {
    $process.Kill($true)
    $stop = [System.Diagnostics.ProcessStartInfo]::new()
    $stop.FileName = 'docker'
    $stop.UseShellExecute = $false
    foreach ($argument in @('stop', '--time', '5', $containerName)) {
        $stop.ArgumentList.Add($argument)
    }
    $stopProcess = [System.Diagnostics.Process]::Start($stop)
    if (-not $stopProcess.WaitForExit(30000)) {
        $stopProcess.Kill($true)
    }
    throw "Docker deadline exceeded ($DockerDeadlineSeconds seconds); fixture $containerName retained for inspection"
}
exit $process.ExitCode
