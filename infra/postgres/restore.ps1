param(
    [Parameter(Mandatory = $false)]
    [string]$DatabaseUrl = $env:DATABASE_URL,
    [Parameter(Mandatory = $true)]
    [string]$BackupFile
)

$ErrorActionPreference = "Stop"
if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) {
    throw "DATABASE_URL or -DatabaseUrl is required"
}
foreach ($command in @("psql", "pg_restore")) {
    if (-not (Get-Command $command -ErrorAction SilentlyContinue)) {
        throw "$command is not available on PATH"
    }
}

$resolvedBackup = (Resolve-Path -LiteralPath $BackupFile).Path
$checksumPath = "$resolvedBackup.sha256"
if (-not (Test-Path -LiteralPath $checksumPath)) {
    throw "Checksum file is missing: $checksumPath"
}
$expectedHash = ((Get-Content -LiteralPath $checksumPath -Raw).Trim() -split "\s+")[0].ToLowerInvariant()
$actualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $resolvedBackup).Hash.ToLowerInvariant()
if ($actualHash -ne $expectedHash) {
    throw "Backup checksum mismatch"
}

$tableCount = (& psql --dbname=$DatabaseUrl --tuples-only --no-align --set=ON_ERROR_STOP=1 --command="SELECT count(*) FROM pg_tables WHERE schemaname = 'public';").Trim()
if ($LASTEXITCODE -ne 0) {
    throw "Could not inspect restore target"
}
if ([int]$tableCount -ne 0) {
    throw "Restore target is not empty; create a fresh database before restoring"
}

& pg_restore --dbname=$DatabaseUrl --exit-on-error --no-owner --no-acl $resolvedBackup
if ($LASTEXITCODE -ne 0) {
    throw "pg_restore failed with exit code $LASTEXITCODE"
}
Write-Output "Restore completed from $resolvedBackup"
