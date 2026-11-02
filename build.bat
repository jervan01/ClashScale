@echo off
chcp 65001 >nul
cd /d "%~dp0"
echo [1/2] 整理依赖...
go mod tidy || goto :err
echo [2/2] 编译 ClashScale...
wails build -clean -platform windows/amd64 -ldflags "-s -w" || goto :err
echo.
echo 完成：build\bin\ClashScale.exe
pause
exit /b 0
:err
echo 构建失败，请检查上面的报错。
pause
exit /b 1
