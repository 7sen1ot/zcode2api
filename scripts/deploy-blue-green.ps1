param(
    [Parameter(Mandatory = $true)]
    [string]$RemoteHost,
    [Parameter(Mandatory = $true)]
    [string]$RemoteUser,
    [Parameter(Mandatory = $true)]
    [string]$Password,
    [string]$Binary = "bin/zcode2api-linux-amd64",
    [string]$Patch = "patches/pro-zai-import.patch",
    [string]$Stamp = (Get-Date -Format "yyyyMMdd-HHmmss")
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$BinaryPath = Join-Path $Root $Binary
$PatchPath = Join-Path $Root $Patch
if (-not (Test-Path $BinaryPath)) {
    throw "Binary not found: $BinaryPath"
}
if (-not (Test-Path $PatchPath)) {
    throw "Patch not found: $PatchPath"
}

$CandidateDir = "/opt/zcode2api-candidate-$Stamp"
$CandidateData = "/var/lib/zcode2api-candidate-$Stamp"
$ServiceName = "zcode2api-candidate-$Stamp"
$CandidateWeb = 6792
$CandidateGateway = 6783
$RemoteBinary = "/home/$RemoteUser/zcode2api-$Stamp-linux-amd64"
$RemotePatch = "/home/$RemoteUser/pro-zai-import-$Stamp.patch"

$Python = @'
import hashlib
import os
import posixpath
import sys
import time
import paramiko

host, user, password, local_binary, local_patch = sys.argv[1:6]
stamp, service, candidate_dir, candidate_data = sys.argv[6:10]
web_port, gateway_port = int(sys.argv[10]), int(sys.argv[11])
remote_binary = f"/home/{user}/zcode2api-{stamp}-linux-amd64"
remote_patch = f"/home/{user}/pro-zai-import-{stamp}.patch"

def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()

client = paramiko.SSHClient()
client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
client.connect(host, username=user, password=password, timeout=15,
               auth_timeout=15, banner_timeout=15)
sftp = client.open_sftp()
sftp.put(local_binary, remote_binary)
sftp.chmod(remote_binary, 0o755)
sftp.put(local_patch, remote_patch)
sftp.close()
local_digest = digest(local_binary)

def run(command, timeout=120):
    stdin, stdout, stderr = client.exec_command(command, timeout=timeout)
    out = stdout.read().decode("utf-8", "replace")
    err = stderr.read().decode("utf-8", "replace")
    status = stdout.channel.recv_exit_status()
    if out:
        print(out, end="")
    if err:
        print(err, end="", file=sys.stderr)
    if status != 0:
        raise RuntimeError(f"remote command failed ({status}): {command}")

run(f"test \"$(sha256sum {remote_binary} | awk '{{print $1}}')\" = {local_digest}")
run(
    "set -e; "
    f"sudo install -d -o zcode2api -g zcode2api -m 700 {candidate_data}; "
    f"sudo find /var/lib/zcode2api-green -mindepth 1 -maxdepth 1 ! -name 'zcode2api.db*' -exec cp -a {{}} {candidate_data}/ \\;; "
    f"sudo python3 - <<'SQLITE'\n"
    "import sqlite3\n"
    "src=sqlite3.connect('file:/var/lib/zcode2api-green/zcode2api.db?mode=ro', uri=True)\n"
    f"dst=sqlite3.connect('{candidate_data}/zcode2api.db')\n"
    "src.backup(dst)\n"
    "dst.close()\n"
    "src.close()\n"
    "SQLITE\n"
    f"sudo chown -R zcode2api:zcode2api {candidate_data}; "
    f"sudo chmod 700 {candidate_data}; "
    f"sudo install -d -o root -g zcode2api -m 750 {candidate_dir}; "
    f"sudo install -o root -g root -m 755 {remote_binary} {candidate_dir}/zcode2api; "
    f"sudo install -o root -g zcode2api -m 640 /opt/zcode2api-green/config.json {candidate_dir}/config.json; "
    f"sudo python3 -c \"import json; p='{candidate_dir}/config.json'; d=json.load(open(p)); d['web_port']={web_port}; d['gateway_port']={gateway_port}; json.dump(d,open(p,'w'),ensure_ascii=False,indent=2)\"; "
    f"sudo tee /etc/systemd/system/{service}.service >/dev/null <<'UNIT'\n"
    "[Unit]\nDescription=zcode2api candidate\nAfter=network-online.target\nWants=network-online.target\n\n"
    "[Service]\nType=simple\nUser=zcode2api\nGroup=zcode2api\n"
    f"WorkingDirectory={candidate_dir}\nEnvironment=TZ=Asia/Shanghai\nEnvironment=HOME={candidate_data}\n"
    f"Environment=ZCODE2API_DATA_DIR={candidate_data}\nExecStart={candidate_dir}/zcode2api --root {candidate_dir}\n"
    "Restart=on-failure\nRestartSec=3\nUMask=0077\nNoNewPrivileges=true\nPrivateTmp=true\nProtectSystem=strict\n"
    f"ProtectHome=true\nReadWritePaths={candidate_data}\nLimitNOFILE=65536\n"
    "UNIT\n"
    "sudo systemctl daemon-reload; "
    f"sudo systemctl enable --now {service}.service"
)
run(
    "set -e; for i in $(seq 1 20); do "
    f"curl -fsS --max-time 3 http://127.0.0.1:{web_port}/healthz && break; sleep 1; done; "
    f"systemctl is-active {service}.service; "
    f"sudo python3 - <<'PY'\n"
    f"import sqlite3; d=sqlite3.connect('{candidate_data}/zcode2api.db'); print('candidate_accounts',d.execute('select count(*) from accounts').fetchone()[0]); d.close()\n"
    "PY"
)
print(f"candidate_ready={candidate_dir} service={service} web_port={web_port}")

# Keep the candidate available. Traffic switching is a separate, explicit step.
client.close()
'@

$temp = [System.IO.Path]::GetTempFileName() + ".py"
try {
    [System.IO.File]::WriteAllText($temp, $Python, [System.Text.UTF8Encoding]::new($false))
    python $temp $RemoteHost $RemoteUser $Password $BinaryPath $PatchPath $Stamp $ServiceName $CandidateDir $CandidateData $CandidateWeb $CandidateGateway
    if ($LASTEXITCODE -ne 0) {
        throw "Blue-green candidate deployment failed"
    }
} finally {
    Remove-Item -Force $temp -ErrorAction SilentlyContinue
}

Write-Host "Candidate started. After health/import checks, switch Nginx to the candidate ports."
Write-Host "Previous Nginx config: /etc/nginx/sites-available/zcode2api-cutover"
Write-Host "Rollback: restore the backup, run nginx -t, then systemctl reload nginx."
