# MetalWatch 前端启动脚本（本地开发/演示）
# 用法：双击 start-frontend.bat；Ctrl+C 停止 dev server
# 幂等：5173 已监听时跳过；node_modules 缺失时自动安装依赖
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..\frontend")

if (-not (Test-Path "node_modules")) {
    Write-Host "[初始化] 安装前端依赖（首次较慢）..."
    npm install --no-audit --no-fund
    if ($LASTEXITCODE -ne 0) { Write-Host "[失败] 依赖安装出错，请检查 Node/npm 环境"; Read-Host "回车退出"; exit 1 }
}

$listening = Get-NetTCPConnection -LocalPort 5173 -State Listen -ErrorAction SilentlyContinue
if ($listening) {
    Write-Host "[跳过] 端口 5173 已被监听，前端可能已在运行（按端口 PID 结束 node.exe 可停止）"
    Read-Host "回车退出"
    exit 0
}

Write-Host "[启动] MetalWatch 前端 http://localhost:5173（Ctrl+C 停止）"
npm run dev
Write-Host "[退出] 前端已停止"
Read-Host "回车退出"
