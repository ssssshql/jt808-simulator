; JT808 模拟器 安装包（按用户安装，不需要管理员权限）
; makensis -DVERSION=1.2.3 -DSRC=dist/jt808sim.exe -DOUT=dist/jt808sim-1.2.3-setup.exe build/installer.nsi
Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"

; 压缩设置必须写在任何 Function / Section 之前（否则报 can't change compressor）
SetCompressor /SOLID lzma

; WM_CLOSE 已由 MUI2 间接包含的 WinMessages.nsh 定义，重复 !define 会报 already defined
!ifndef WM_CLOSE
  !define WM_CLOSE 0x0010
!endif

; 优雅关闭正在运行的实例：先给主窗口发 WM_CLOSE（触发状态保存），
; 最多等 6 秒；窗口仍在才回退到强杀（兜底，处理无响应的情况）。
; 安装段与卸载段各调一次，卸载段函数名必须带 un. 前缀，因此本体写成宏
!macro CloseAppBody
  FindWindow $0 "jt808simWindow"
  ${If} $0 <> 0
    SendMessage $0 ${WM_CLOSE} 0 0
    StrCpy $1 0
    ${Do}
      Sleep 500
      IntOp $1 $1 + 1
      FindWindow $0 "jt808simWindow"
      ${If} $0 == 0
        ${ExitDo}
      ${EndIf}
      ${If} $1 >= 12
        ${ExitDo}
      ${EndIf}
    ${Loop}
    ${If} $0 <> 0
      nsExec::Exec 'taskkill /F /IM jt808sim.exe'
      Sleep 1000
    ${EndIf}
  ${EndIf}
!macroend

Function CloseAppGracefully
  !insertmacro CloseAppBody
FunctionEnd

Function un.CloseAppGracefully
  !insertmacro CloseAppBody
FunctionEnd

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!define APPNAME "JT808 模拟器"
!define APPID   "jt808sim"
!define UNKEY   "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPID}"

Name "${APPNAME} ${VERSION}"
OutFile "${OUT}"
InstallDir "$LOCALAPPDATA\Programs\${APPID}"
InstallDirRegKey HKCU "Software\${APPID}" "InstallDir"
RequestExecutionLevel user

!define MUI_ICON   "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_FINISHPAGE_RUN "$INSTDIR\jt808sim.exe"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

Section "Install"
  ; 覆盖安装前结束正在运行的实例（优雅关闭，保留终端状态）
  Call CloseAppGracefully
  SetOutPath "$INSTDIR"
  File "/oname=jt808sim.exe" "${SRC}"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  CreateShortCut "$SMPROGRAMS\${APPNAME}.lnk" "$INSTDIR\jt808sim.exe"
  CreateShortCut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\jt808sim.exe"
  WriteRegStr HKCU "Software\${APPID}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNKEY}" "DisplayName" "${APPNAME}"
  WriteRegStr HKCU "${UNKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNKEY}" "Publisher" "ssssshql"
  WriteRegStr HKCU "${UNKEY}" "DisplayIcon" "$INSTDIR\jt808sim.exe"
  WriteRegStr HKCU "${UNKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKCU "${UNKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNKEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  Call un.CloseAppGracefully
  Delete "$INSTDIR\jt808sim.exe"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\${APPNAME}.lnk"
  Delete "$DESKTOP\${APPNAME}.lnk"
  DeleteRegKey HKCU "${UNKEY}"
  DeleteRegKey HKCU "Software\${APPID}"
  ; 终端状态保存在 %APPDATA%\jt808sim，卸载时保留
SectionEnd
