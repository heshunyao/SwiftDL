@echo off
setlocal
cd /d "%~dp0"

if not exist bin mkdir bin

echo [1/2] 编译 SwiftDL ...
go build -o bin\swiftdl.exe .\cmd\swiftdl
if errorlevel 1 (
    echo 编译失败，请检查 Go 环境。
    pause
    exit /b 1
)

echo [2/2] 启动服务，浏览器将自动打开 http://127.0.0.1:8765
start "" bin\swiftdl.exe
