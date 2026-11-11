$ErrorActionPreference = 'Stop'
docker compose run --rm acceptance --phase before
if ($LASTEXITCODE -ne 0) { throw 'Acceptance before restart failed' }
docker compose restart matchcore
if ($LASTEXITCODE -ne 0) { throw 'Engine restart failed' }
$deadline = (Get-Date).AddSeconds(60)
do {
    Start-Sleep -Seconds 1
    docker compose exec -T gateway /app/matchcore health http://localhost:8080/readyz *> $null
    $ready = $LASTEXITCODE -eq 0
} until ($ready -or (Get-Date) -gt $deadline)
if (-not $ready) { throw 'Gateway did not recover after restart' }
docker compose run --rm acceptance --phase after
if ($LASTEXITCODE -ne 0) { throw 'Acceptance after restart failed' }
