$ErrorActionPreference = "Stop"

$dist = Join-Path (Split-Path -Parent $PSScriptRoot) "dist"
$expected = @{}
Get-Content (Join-Path $dist "SHA256SUMS") | ForEach-Object {
    if ($_ -match '^([0-9a-f]{64})\s+(.+)$') {
        $expected[$matches[2]] = $matches[1]
    }
}
foreach ($name in $expected.Keys) {
    $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $dist $name)).Hash.ToLowerInvariant()
    if ($actual -ne $expected[$name]) { throw "checksum mismatch: $name" }
}

$executable = Join-Path $dist "relay-server-windows-amd64.exe"
$version = & $executable --version
if ($version -ne "0.7.1") { throw "Windows version was $version" }

$testRoot = Join-Path $env:TEMP ("relay-win-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $testRoot | Out-Null
try {
    $start = [System.Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $executable
    $start.Arguments = "--config `"$testRoot\relay.json`" --data-dir `"$testRoot\data`" --listen 127.0.0.1 --port 18087"
    $start.UseShellExecute = $false
    $start.RedirectStandardInput = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.CreateNoWindow = $true
    $start.Environment["RELAY_SUPABASE_URL"] = "http://127.0.0.1:1"
    $start.Environment["RELAY_SUPABASE_PUBLISHABLE_KEY"] = "test-publishable"

    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $start
    $null = $process.Start()

    $health = $null
    for ($attempt = 0; $attempt -lt 50; $attempt++) {
        Start-Sleep -Milliseconds 100
        try {
            $health = Invoke-RestMethod -Uri "http://127.0.0.1:18087/health" -TimeoutSec 1
            if ($health.status -eq "ok") { break }
        } catch {}
    }
    if ($null -eq $health -or $health.status -ne "ok") {
        $process.Kill()
        throw "Windows artifact health check failed"
    }

    $process.StandardInput.WriteLine("status")
    $process.StandardInput.WriteLine("stop")
    if (-not $process.WaitForExit(10000)) {
        $process.Kill()
        throw "Windows artifact did not stop"
    }
    $stdout = $process.StandardOutput.ReadToEnd()
    $stderr = $process.StandardError.ReadToEnd()
    if ($process.ExitCode -ne 0 -or $stdout -notmatch "RELAY READY" -or $stdout -notmatch "STATUS ready=true" -or $stdout -notmatch "shutdown complete") {
        throw "Windows runtime failed: exit=$($process.ExitCode), stdout=$stdout, stderr=$stderr"
    }
    Write-Output "Windows artifact: version=$version health=$($health.status) exit=$($process.ExitCode)"
} finally {
    if ($null -ne $process -and -not $process.HasExited) { $process.Kill() }
    Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
}
