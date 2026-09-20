<#
.SYNOPSIS
  Seeds the sqit test databases (sqit_local, sqit_upstream) into an EXISTING
  MySQL server running in a Docker container, e.g. your rendszer-mysql.
  Re-running resets both databases to the seed state. Nothing else on the
  server is touched.

.EXAMPLE
  .\scripts\setup-existing-mysql.ps1 -Container rendszer-mysql -RootPassword qwe123
#>
param(
  [string]$Container = "rendszer-mysql",
  [string]$RootUser = "root",
  [Parameter(Mandatory = $true)][string]$RootPassword
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$files = @(
  "$root\testdata\existing-server\00-schema.sql",
  "$root\testdata\10-local.sql",
  "$root\testdata\20-upstream.sql"
)
foreach ($f in $files) {
  Write-Host "applying $(Split-Path -Leaf $f) ..."
  Get-Content $f -Raw | docker exec -i $Container mysql "-u$RootUser" "-p$RootPassword"
  if ($LASTEXITCODE -ne 0) { throw "mysql failed on $f" }
}
docker exec $Container mysql "-u$RootUser" "-p$RootPassword" -e "SELECT 'sqit_local' db, id, name, email, active FROM sqit_local.users; SHOW GRANTS FOR 'sqit_upstream'@'%';"
Write-Host "done. sqit_local and sqit_upstream are seeded."
