# Instala Kronos — memoria persistente para agentes de IA
# Uso: irm https://raw.githubusercontent.com/jjgarcia-app/kronos-v2/main/install.ps1 | iex
#
# O con opciones:
#   $env:KRONOS_DIR = "C:\tools"; irm .../install.ps1 | iex

$ErrorActionPreference = "Stop"
$REPO = "jjgarcia-app/kronos-v2"

# ── Detectar arquitectura ─────────────────────────────────────────────────────
$arch = if ([System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture -eq "Arm64") {
    "arm64"
} else {
    "amd64"
}

# ── Obtener última versión ────────────────────────────────────────────────────
Write-Host "Buscando la ultima version de Kronos..."
try {
    $release = Invoke-RestMethod "https://api.github.com/repos/$REPO/releases/latest"
    $version = $release.tag_name
} catch {
    Write-Error "No se pudo obtener la ultima version. Verifica tu conexion."
    Write-Host "Descarga manualmente de: https://github.com/$REPO/releases"
    exit 1
}

Write-Host "Version: $version  |  Sistema: windows/$arch"

# ── Directorio de instalacion ─────────────────────────────────────────────────
$installDir = if ($env:KRONOS_DIR) {
    $env:KRONOS_DIR
} else {
    "$env:USERPROFILE\bin"
}
New-Item -ItemType Directory -Force $installDir | Out-Null

# ── Descargar ─────────────────────────────────────────────────────────────────
$url = "https://github.com/$REPO/releases/download/$version/kronos_windows_${arch}.zip"
$tmpZip = "$env:TEMP\kronos_install.zip"
$tmpDir = "$env:TEMP\kronos_install"

Write-Host "Descargando $url..."
try {
    Invoke-WebRequest -Uri $url -OutFile $tmpZip -UseBasicParsing
} catch {
    Write-Error "Error al descargar: $_"
    exit 1
}

# ── Extraer ───────────────────────────────────────────────────────────────────
if (Test-Path $tmpDir) { Remove-Item $tmpDir -Recurse -Force }
Expand-Archive -Path $tmpZip -DestinationPath $tmpDir -Force

# ── Instalar ──────────────────────────────────────────────────────────────────
$src = Join-Path $tmpDir "kronos.exe"
$dst = Join-Path $installDir "kronos.exe"
Move-Item -Path $src -Destination $dst -Force

Remove-Item $tmpZip -Force
Remove-Item $tmpDir -Recurse -Force

# ── Agregar al PATH del usuario si no está ────────────────────────────────────
$userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
if ($userPath -notlike "*$installDir*") {
    [Environment]::SetEnvironmentVariable("PATH", "$userPath;$installDir", "User")
    Write-Host "Directorio agregado al PATH de usuario: $installDir"
    $env:PATH += ";$installDir"
}

Write-Host ""
Write-Host "OK Kronos $version instalado en $dst"
Write-Host ""

# ── Lanzar wizard de configuracion ────────────────────────────────────────────
if (Get-Command kronos -ErrorAction SilentlyContinue) {
    Write-Host "Iniciando configuracion..."
    Write-Host ""
    & kronos init
} else {
    # PATH aun no cargado en esta sesion — ejecutar con ruta completa
    Write-Host "Iniciando configuracion..."
    Write-Host ""
    & $dst init
}
