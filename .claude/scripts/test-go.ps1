param(
    [switch] $Short,
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
& docker @dockerArguments
exit $LASTEXITCODE
