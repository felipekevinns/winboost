@echo off
chcp 65001 >nul
cd /d "%~dp0"
title Publicar WinBoost no GitHub
echo ============================================================
echo   Publicando o WinBoost no GitHub (felipekevinns/winboost)
echo ============================================================
echo.
echo ANTES DE CONTINUAR: crie um repositorio VAZIO chamado "winboost"
echo em  https://github.com/new  (NAO marque "Add a README").
echo.
pause

where git >nul 2>&1
if errorlevel 1 (
  echo.
  echo ERRO: o Git nao esta instalado. Baixe em https://git-scm.com/download/win
  echo.
  pause
  exit /b 1
)

rem Coloca o workflow de build automatico no lugar certo
if not exist ".github\workflows" mkdir ".github\workflows"
if exist "build.yml.txt" copy /y "build.yml.txt" ".github\workflows\build.yml" >nul

if not exist ".git" (
  echo Iniciando o repositorio local...
  git init -b main
  git add -A
  git -c user.email=automaxcodebr@gmail.com -c user.name=felipekevinns commit -m "WinBoost 1.0.0 - otimizador de Windows"
)

git remote remove origin >nul 2>&1
git remote add origin https://github.com/felipekevinns/winboost.git

echo.
echo Enviando o codigo... (pode abrir o navegador para voce ENTRAR no GitHub)
git push -u origin main
if errorlevel 1 (
  echo.
  echo Algo deu errado no envio. Confira se o repositorio "winboost" existe
  echo e esta VAZIO em https://github.com/felipekevinns/winboost
  echo.
  pause
  exit /b 1
)

echo.
echo Gerando a versao v1.0.0 (o GitHub vai compilar o .exe e publicar em Releases)...
git tag -f v1.0.0
git push -f origin v1.0.0

echo.
echo ============================================================
echo   PRONTO!
echo   Codigo:   https://github.com/felipekevinns/winboost
echo   Download: https://github.com/felipekevinns/winboost/releases
echo   (o .exe leva uns 2 minutos para aparecer em Releases)
echo ============================================================
echo.
pause
