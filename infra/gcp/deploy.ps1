param(
    [Parameter(Mandatory=$true)][ValidatePattern('^[a-z][a-z0-9-]{4,28}[a-z0-9]$')][string]$ProjectId,
    [Parameter(Mandatory=$true)][ValidatePattern('^[a-z]+-[a-z]+[0-9]+-[a-z]$')][string]$Zone
)
$ErrorActionPreference='Stop'
$taskRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$taskArchive = Join-Path $taskRoot '.cache\gateforge-deploy.tar.gz'
New-Item -ItemType Directory -Path (Split-Path -Parent $taskArchive) -Force | Out-Null
Push-Location $taskRoot
try {
    & tar -czf $taskArchive --exclude=web/node_modules --exclude=web/dist --exclude='*.pem' Dockerfile .dockerignore compose.yaml compose.gcp.yaml go.mod go.sum cmd internal examples configs web
    if ($LASTEXITCODE -ne 0) { throw 'Could not package the source.' }
    & gcloud compute scp $taskArchive 'gateforge:gateforge-deploy.tar.gz' "--project=$ProjectId" "--zone=$Zone" --tunnel-through-iap
    if ($LASTEXITCODE -ne 0) { throw 'Upload failed.' }
    $taskRemote = 'set -eu; sudo test -s /opt/gateforge/.env; sudo test -s /opt/gateforge/certs/cert.pem; sudo test -s /opt/gateforge/certs/key.pem; sudo tar -xzf gateforge-deploy.tar.gz -C /opt/gateforge/app; cd /opt/gateforge/app; sudo docker compose --env-file /opt/gateforge/.env -f compose.yaml -f compose.gcp.yaml up --build -d --wait'
    & gcloud compute ssh gateforge "--project=$ProjectId" "--zone=$Zone" --tunnel-through-iap "--command=$taskRemote"
    if ($LASTEXITCODE -ne 0) { throw 'Deployment failed; inspect container logs before retrying.' }
} finally { Pop-Location }
