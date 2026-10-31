@echo off
chcp 65001 >nul
cd /d "%~dp0"

if not defined GO set GO=d:\app\Go\bin\go.exe
if not exist "%GO%" set GO=go

echo [1/2] 构建界面 -^> client\web
cd ui
call node .\node_modules\vite\bin\vite.js build
if errorlevel 1 goto err
cd ..

echo [2/2] 构建客户端 -^> client\edu.exe
set GOFLAGS=-mod=mod
"%GO%" build -ldflags "-H windowsgui -s -w" -o client\edu.exe .
if errorlevel 1 goto err

echo.
echo 完成：client\edu.exe
echo   同级 web\      界面文件（构建产物）
echo   同级 data\     账号 / 设置（首次运行自动创建）
goto end

:err
echo.
echo 构建失败
exit /b 1

:end
