# MetalWatch 后端启动脚本（本地开发/演示）
# 用法：双击 start-backend.bat 或 powershell -File start-backend.ps1；Ctrl+C 停止
# 幂等：18080 已监听时跳过；二进制缺失时自动构建（D15 start 幂等约定）
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..\backend")

if (-not (Test-Path "bin-local\metalwatch.exe")) {
    Write-Host "[构建] 未找到二进制，开始编译..."
    go build -o bin-local\metalwatch.exe ./cmd/server
    if ($LASTEXITCODE -ne 0) { Write-Host "[失败] 构建出错，请检查 Go 环境"; Read-Host "回车退出"; exit 1 }
}

$listening = Get-NetTCPConnection -LocalPort 18080 -State Listen -ErrorAction SilentlyContinue
if ($listening) {
    Write-Host "[跳过] 端口 18080 已被监听，后端可能已在运行（结束 metalwatch.exe 可停止）"
    Read-Host "回车退出"
    exit 0
}

if (-not (Test-Path "tmp-data\bootstrap_admin.txt")) {
    Write-Host "[提示] 全新数据目录：初始管理员口令见 tmp-data\bootstrap_admin.txt（启动后生成）"
}

# 不传 --listen：监听地址由 main 按环境决定（dev 只听 127.0.0.1，见 docs/01 D35）。
# 这里原先硬写 0.0.0.0:18080，会让开发机对整个局域网开放登录页，绕过该决策。
Write-Host "[启动] MetalWatch 后端 http://127.0.0.1:18080（数据目录 backend\tmp-data，Ctrl+C 停止）"
& .\bin-local\metalwatch.exe serve --data tmp-data
Write-Host "[退出] 后端已停止"
Read-Host "回车退出"
