; JT808 模拟器 安装包（按用户安装，不需要管理员权限）
; makensis -DVERSION=1.2.3 -DSRC=dist/jt808sim.exe -DOUT=dist/jt808sim-1.2.3-setup.exe build/installer.nsi
Unicode true
!include "MUI2.nsh"

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
SetCompressor /SOLID lzma

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
  ; 覆盖安装前结束正在运行的实例
  nsExec::Exec 'taskkill /F /IM jt808sim.exe'
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
  nsExec::Exec 'taskkill /F /IM jt808sim.exe'
  Delete "$INSTDIR\jt808sim.exe"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\${APPNAME}.lnk"
  Delete "$DESKTOP\${APPNAME}.lnk"
  DeleteRegKey HKCU "${UNKEY}"
  DeleteRegKey HKCU "Software\${APPID}"
  ; 终端状态保存在 %APPDATA%\jt808sim，卸载时保留
SectionEnd
