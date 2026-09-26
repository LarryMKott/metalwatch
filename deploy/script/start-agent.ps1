# MetalWatch Agent 启动脚本（本机接入演示，gRPC 流模式）
# 用法：双击 start-agent.bat；Ctrl+C 停止
# 前提：已用一次性注册码完成注册（令牌落盘 agent\bin-local\token）
# 幂等：检测到 agent.exe 进程已存在时跳过
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..\agent")

if (-not (Test-Path "bin-local\agent.exe")) {
    Write-Host "[构建] 未找到二进制，开始编译..."
    go build -o bin-local\agent.exe ./cmd/windows
    if ($LASTEXITCODE -ne 0) { Write-Host "[失败] 构建出错，请检查 Go 环境"; Read-Host "回车退出"; exit 1 }
}

if (Get-Process agent -ErrorAction SilentlyContinue) {
    Write-Host "[跳过] Agent 进程已存在（结束 agent.exe 可停止）"
    Read-Host "回车退出"
    exit 0
}

# 凭据不再由脚本 export：开发环境 Agent 会自己读 bin-local\credentials.json（docs/01 D35）。
# 原先这里的 host_id 兜底 `= "1"` 会覆盖凭据里的真实主机 ID（注册分配的通常不是 1），
# 结果以错误身份上报、服务端返回 403，且报错指向令牌而非 ID，把排障方向带偏。
if (-not (Test-Path "bin-local\credentials.json") -and -not (Test-Path "bin-local\token")) {
    Write-Host "[提示] 未找到本地凭据（bin-local\credentials.json 或 token）。请先用注册码完成注册："
    Write-Host '  cd backend && go run ./cmd/server agent-code --data tmp-data --days 7'
    Write-Host '  bin-local\agent.exe --server http://127.0.0.1:18080 --code <注册码> --spool bin-local\spool'
    Read-Host "回车退出"
    exit 1
}

Write-Host "[启动] Agent（gRPC 流模式，凭据自动读取本地，Ctrl+C 停止）"
& .\bin-local\agent.exe --server http://127.0.0.1:18080 --spool bin-local\spool --interval 15s
Write-Host "[退出] Agent 已停止"
Read-Host "回车退出"
