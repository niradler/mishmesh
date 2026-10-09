$ErrorActionPreference = 'Stop'
$context = & kubectl config current-context
if ($LASTEXITCODE -or $context -ne 'k3d-mmhelm') {
    throw 'This smoke check requires the isolated k3d-mmhelm test cluster'
}

function Invoke-ProbeCurl {
    param([string[]] $Arguments)
    $output = & kubectl --request-timeout=30s exec -n mm-beta-proof kit -- curl --max-time 20 @Arguments
    if ($LASTEXITCODE) {
        throw "Probe curl failed with exit code $LASTEXITCODE"
    }
    return ($output -join "`n").Trim()
}

$api = 'http://mm-beta-api:8081'
$cookie = '/tmp/mm-beta-cookie'
$body = '/tmp/mm-beta-account.json'
$registration = '{"email":"beta-proof@example.test","password":"beta-proof-test-only-password","name":"Beta Proof"}'
$login = '{"email":"beta-proof@example.test","password":"beta-proof-test-only-password"}'
$unauthenticated = Invoke-ProbeCurl @('-sS', '-o', '/dev/null', '-w', '%{http_code}', "$api/api/v1/endpoints")
if ($unauthenticated -ne '401') {
    throw "Unauthenticated API status $unauthenticated"
}
Write-Host 'PASS unauthenticated control API rejected (401)'

$created = Invoke-ProbeCurl @('-sS', '-o', $body, '-c', $cookie, '-w', '%{http_code}', '-H', 'Content-Type: application/json', '-H', "Origin: $api", '--data-binary', $registration, "$api/api/v1/auth/register")
if ($created -eq '409') {
    $created = Invoke-ProbeCurl @('-sS', '-o', $body, '-c', $cookie, '-w', '%{http_code}', '-H', 'Content-Type: application/json', '-H', "Origin: $api", '--data-binary', $login, "$api/api/v1/auth/login")
}
if ($created -notin @('200', '201')) {
    throw "Registration/login status $created"
}
$me = (Invoke-ProbeCurl @('-fsS', '-b', $cookie, "$api/api/v1/auth/me")) | ConvertFrom-Json
if ($me.role -ne 'owner' -or $me.active_org_id -ne 'org_default') {
    throw 'First-owner session mismatch'
}
Write-Host 'PASS first-owner session'

$endpoints = (Invoke-ProbeCurl @('-fsS', '-b', $cookie, "$api/api/v1/endpoints")) | ConvertFrom-Json
if (@($endpoints).Count -ne 1 -or $endpoints.public_url -match '/tunnel/') {
    throw 'Customer endpoint URL mismatch'
}
Write-Host 'PASS session endpoint access with host URL and no disabled path URL'

$csrf = Invoke-ProbeCurl @('-sS', '-o', '/dev/null', '-w', '%{http_code}', '-b', $cookie, '-H', 'Content-Type: application/json', '-H', 'Origin: https://evil.example.test', '--data-binary', '{"name":"must-not-create"}', "$api/api/v1/orgs")
if ($csrf -ne '403') {
    throw "Cross-origin write status $csrf"
}
Write-Host 'PASS cross-origin cookie write rejected (403)'

$outsider = Invoke-ProbeCurl @('-sS', '-o', '/dev/null', '-w', '%{http_code}', '-H', 'Content-Type: application/json', '-H', "Origin: $api", '--data-binary', '{"email":"beta-outsider@example.test","password":"beta-proof-test-only-password","name":"Must Need Invite"}', "$api/api/v1/auth/register")
if ($outsider -ne '403') {
    throw "Registration without invitation status $outsider"
}
Write-Host 'PASS subsequent registration requires invitation (403)'

$html = Invoke-ProbeCurl @('-fsS', "$api/")
if (-not $html.Contains('id="root"') -or $html -notmatch '<script[^>]+src="([^"]+)"') {
    throw 'Bundled web UI entry point missing'
}
$scriptPath = $Matches[1]
if (-not $scriptPath.StartsWith('/assets/')) {
    throw 'Unexpected web UI script path'
}
$asset = Invoke-ProbeCurl @('-fsS', '-o', '/dev/null', '-w', '%{content_type} %{size_download}', "$api$scriptPath")
if ($asset -notmatch '^(?:application|text)/javascript(?:;[^\r\n]*)? ([0-9]+)$' -or [int]$Matches[1] -lt 100000) {
    throw "Unexpected web UI asset response $asset"
}
Write-Host 'PASS bundled web UI HTML and JavaScript asset'
