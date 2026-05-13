# Convert DOCUMENTATION.md to PDF using the system Chrome.
# - Rewrites relative file links to absolute GitHub URLs (so they stay
#   clickable when the PDF is shared standalone). Images stay local.
# - Renders MD -> HTML via `npx marked` (~100 KB, no Chromium download).
# - Prints the HTML to PDF with Chrome headless.
#
# Usage:  .\build-docs-pdf.ps1
#         .\build-docs-pdf.ps1 -InputPath OTHER.md -OutputPath OTHER.pdf

param(
    [string]$InputPath  = "DOCUMENTATION.md",
    [string]$OutputPath = "DOCUMENTATION.pdf",
    [string]$RepoBase   = "https://github.com/sahurai/CLOUD-Project-1/blob/main"
)

$ErrorActionPreference = "Stop"

# Force UTF-8 for native command stdout (otherwise PowerShell 5.1 decodes
# marked's UTF-8 output through the OEM codepage and breaks em-dashes etc).
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding           = [System.Text.Encoding]::UTF8

if (-not (Test-Path $InputPath)) { throw "Input file not found: $InputPath" }

# --- Locate Chrome ---
$chromeCandidates = @(
    "C:\Program Files\Google\Chrome\Application\chrome.exe",
    "C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
    "C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
    "C:\Program Files\Microsoft\Edge\Application\msedge.exe"
)
$chrome = $chromeCandidates | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $chrome) { throw "Neither Chrome nor Edge found in standard locations." }

$inputAbs  = (Resolve-Path $InputPath).Path
$workDir   = Split-Path $inputAbs -Parent
$baseName  = [System.IO.Path]::GetFileNameWithoutExtension($InputPath)
$exportMd  = Join-Path $workDir ("$baseName.export.md")
$exportHtm = Join-Path $workDir ("$baseName.export.html")

# --- 1. Rewrite relative links to GitHub URLs ---
# Force UTF-8: PS 5.1's Get-Content default is the local ANSI codepage on
# files without BOM, which silently mojibakes em-dashes, §, ≥ etc.
$content = Get-Content -Raw -Encoding UTF8 $InputPath
$linkPattern = '(?<!\!)\[([^\]]+)\]\(([^)]+)\)'
$rewritten = [regex]::Replace($content, $linkPattern, {
    param($m)
    $text = $m.Groups[1].Value
    $url  = $m.Groups[2].Value
    if ($url -match '^(https?:|mailto:|#)') { return $m.Value }
    $url = $url -replace '^\./', ''
    return "[$text]($RepoBase/$url)"
})
Set-Content -Path $exportMd -Value $rewritten -Encoding utf8
Write-Host "[1/3] Rewrote links -> $exportMd"

# --- 2. MD -> HTML via marked (tiny dep, downloads once into npx cache) ---
$bodyHtml = & npx --yes marked@latest --gfm --input $exportMd
if ($LASTEXITCODE -ne 0) { throw "marked failed (exit $LASTEXITCODE)" }
$bodyHtml = $bodyHtml -join "`n"

# Marked v5+ does not auto-emit `id` attrs on headings, so anchor links
# (#1-project-goal-and-focus) have nowhere to jump. Inject IDs ourselves
# using the GitHub-Flavored-Markdown slug algorithm:
#   1. inner text only (strip nested tags)
#   2. lowercase
#   3. whitespace -> "-"
#   4. drop anything that isn't [a-z 0-9 _ -]
function Get-GfmSlug {
    param([string]$text)
    $t = [regex]::Replace($text, '<[^>]+>', '')   # strip tags
    $t = [System.Net.WebUtility]::HtmlDecode($t)
    $t = $t.ToLowerInvariant()
    $t = $t -replace '\s+', '-'
    $t = $t -replace '[^a-z0-9_\-]', ''
    return $t
}
$bodyHtml = [regex]::Replace($bodyHtml, '<h([1-6])>([\s\S]*?)</h\1>', {
    param($m)
    $level = $m.Groups[1].Value
    $inner = $m.Groups[2].Value
    $slug  = Get-GfmSlug $inner
    return "<h$level id=`"$slug`">$inner</h$level>"
})

$css = @'
<style>
  @page { size: A4; margin: 18mm 16mm; }
  body  { font-family: -apple-system, "Segoe UI", Roboto, sans-serif;
          color: #24292f; line-height: 1.55; font-size: 11pt; max-width: 100%; }
  h1, h2, h3, h4 { color: #1f2328; line-height: 1.25; margin-top: 1.6em; margin-bottom: 0.4em;
                   page-break-after: avoid; }
  h1 { font-size: 22pt; border-bottom: 1px solid #d0d7de; padding-bottom: .2em; }
  h2 { font-size: 17pt; border-bottom: 1px solid #d0d7de; padding-bottom: .2em; }
  h3 { font-size: 13pt; } h4 { font-size: 11.5pt; }
  p, ul, ol, table { margin: .5em 0; }
  code { font-family: "Cascadia Mono", Consolas, monospace; font-size: 90%;
         background: #f6f8fa; padding: 1px 4px; border-radius: 4px; }
  pre  { background: #f6f8fa; padding: 10px 12px; border-radius: 6px; overflow-x: auto;
         font-size: 86%; page-break-inside: avoid; }
  pre code { background: none; padding: 0; }
  a    { color: #0969da; text-decoration: none; }
  a:hover { text-decoration: underline; }
  table { border-collapse: collapse; width: 100%; font-size: 95%; page-break-inside: avoid; }
  th, td { border: 1px solid #d0d7de; padding: 6px 8px; vertical-align: top; text-align: left; }
  th { background: #f6f8fa; }
  img { max-width: 100%; height: auto; page-break-inside: avoid; }
  blockquote { color: #57606a; border-left: 3px solid #d0d7de; margin: .5em 0; padding: 0 1em; }
  hr { border: 0; border-top: 1px solid #d0d7de; margin: 1.5em 0; }
</style>
'@

$html = @"
<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>$([System.IO.Path]::GetFileNameWithoutExtension($InputPath))</title>
$css
</head><body>
$bodyHtml
</body></html>
"@
Set-Content -Path $exportHtm -Value $html -Encoding utf8
Write-Host "[2/3] Rendered HTML -> $exportHtm"

# --- 3. HTML -> PDF via Chrome headless ---
# - URL-encode spaces in the file:// URI (Windows paths often contain them)
# - Use a private --user-data-dir so we don't conflict with a running Chrome
# - Use an absolute output path (headless Chrome ignores PowerShell's CWD)
$absHtml = (Resolve-Path $exportHtm).Path
$absPdf  = if ([System.IO.Path]::IsPathRooted($OutputPath)) { $OutputPath } else { Join-Path $workDir $OutputPath }
$fileUri = "file:///" + (($absHtml -replace '\\','/') -replace ' ','%20')
$userDir = Join-Path ([System.IO.Path]::GetTempPath()) ("chrome-pdf-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $userDir -Force | Out-Null

# Chrome writes "<n> bytes written to file ..." to stderr on success, which
# PowerShell 5.1 treats as an error under $ErrorActionPreference='Stop'.
# Suppress stderr by redirecting it to $null inside a Continue scope.
$prevPref = $ErrorActionPreference
$ErrorActionPreference = "Continue"
try {
    & $chrome --headless --disable-gpu --no-sandbox `
        "--user-data-dir=$userDir" `
        "--print-to-pdf=$absPdf" $fileUri 2>$null | Out-Null
} finally {
    $ErrorActionPreference = $prevPref
    Remove-Item $userDir -Recurse -Force -ErrorAction SilentlyContinue
}
if (-not (Test-Path $absPdf)) { throw "Chrome did not produce PDF: $absPdf" }

if (-not $env:KEEP_INTERMEDIATES) { Remove-Item $exportMd, $exportHtm -Force }
$sizeMB = [math]::Round((Get-Item $absPdf).Length / 1MB, 2)
Write-Host "[3/3] Done: $absPdf ($sizeMB MB)"
