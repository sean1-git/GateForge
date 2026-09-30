$ErrorActionPreference = 'Stop'
$taskRoot = Split-Path -Parent $PSScriptRoot
$taskEnvFile = Join-Path $taskRoot '.env'
if (Test-Path -LiteralPath $taskEnvFile) { throw '.env already exists; preserving its credentials.' }
function New-GateForgeSecret {
    $taskBytes = New-Object byte[] 32
    $taskRng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $taskRng.GetBytes($taskBytes) } finally { $taskRng.Dispose() }
    return ([BitConverter]::ToString($taskBytes)).Replace('-', '').ToLowerInvariant()
}
$taskValues = @("POSTGRES_PASSWORD=$(New-GateForgeSecret)", "REDIS_PASSWORD=$(New-GateForgeSecret)", "GATEFORGE_ADMIN_TOKEN=$(New-GateForgeSecret)")
[System.IO.File]::WriteAllLines($taskEnvFile, $taskValues)
Write-Output 'Created .env with random local credentials. Keep it private. Start with: docker compose up --build -d'
