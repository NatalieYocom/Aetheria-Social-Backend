param(
    [Parameter(Mandatory = $false)]
    [string]$DatabaseUrl = $env:DATABASE_URL,
    [string]$BackupDirectory = "./backups",
    [int]$RetentionDays = 14
)

$ErrorActionPreference = "Stop"
if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) {
    throw "DATABASE_URL or -DatabaseUrl is required"
}
if ($RetentionDays -lt 1) {
    throw "RetentionDays must be at least 1"
}
if (-not (Get-Command pg_dump -ErrorAction SilentlyContinue)) {
    throw "pg_dump is not available on PATH"
}

$directory = New-Item -ItemType Directory -Force -Path $BackupDirectory
$timestamp = (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
$finalPath = Join-Path $directory.FullName "basis-social-$timestamp.dump"
$temporaryPath = "$finalPath.partial"

try {
    & pg_dump --dbname=$DatabaseUrl --format=custom --compress=9 --no-owner --no-acl --file=$temporaryPath
    if ($LASTEXITCODE -ne 0) {
        throw "pg_dump failed with exit code $LASTEXITCODE"
    }
    Move-Item -LiteralPath $temporaryPath -Destination $finalPath
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $finalPath).Hash.ToLowerInvariant()
    Set-Content -LiteralPath "$finalPath.sha256" -Value "$hash  $([IO.Path]::GetFileName($finalPath))" -Encoding ascii

    $cutoff = (Get-Date).ToUniversalTime().AddDays(-$RetentionDays)
    Get-ChildItem -LiteralPath $directory.FullName -File |
        Where-Object { $_.LastWriteTimeUtc -lt $cutoff -and ($_.Name -like "basis-social-*.dump" -or $_.Name -like "basis-social-*.dump.sha256") } |
        Remove-Item -Force

    Write-Output $finalPath
} finally {
    if (Test-Path -LiteralPath $temporaryPath) {
        Remove-Item -LiteralPath $temporaryPath -Force
    }
}
