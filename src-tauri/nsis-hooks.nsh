; freev6 installer hooks — wired in via tauri.conf.json:
;   bundle.windows.nsis.installerHooks
;
; NSIS_HOOK_PREINSTALL runs at the top of Section Install, BEFORE the
; template's own CheckIfAppIsRunning and before the File copies. The stock
; check only knows freev6-desktop.exe and answers "OK to kill it"; killing
; the elevated desktop leaves freev6-helper.exe (and mihomo) behind holding
; the installed files, so the copy then dies with a raw write error — the
; user just sees "installation failed".
;
; Instead: remind the user to exit the app properly first (tray 退出 runs
; the quit flow that stops 免流 and the helper) and re-check until both
; processes are gone; cancel aborts with a clear message. Unattended
; installs (/S silent, /P passive) skip this and keep the stock behavior.
;
; FindProcess / FindProcessCurrentUser push 0 = running, 1 = not running
; (tauri-apps/nsis-tauri-utils, crates/nsis-process).

!macro NSIS_HOOK_PREINSTALL
  !define FreeV6HookID ${__LINE__}

  ${If} ${Silent}
  ${OrIf} $PassiveMode = 1
    Goto freev6_hook_done_${FreeV6HookID}
  ${EndIf}

  freev6_hook_retry_${FreeV6HookID}:
    StrCpy $R7 0

    !if "${INSTALLMODE}" == "currentUser"
      nsis_tauri_utils::FindProcessCurrentUser "${MAINBINARYNAME}.exe"
    !else
      nsis_tauri_utils::FindProcess "${MAINBINARYNAME}.exe"
    !endif
    Pop $R8
    ${If} $R8 = 0
      StrCpy $R7 1
    ${EndIf}

    !if "${INSTALLMODE}" == "currentUser"
      nsis_tauri_utils::FindProcessCurrentUser "freev6-helper.exe"
    !else
      nsis_tauri_utils::FindProcess "freev6-helper.exe"
    !endif
    Pop $R8
    ${If} $R8 = 0
      StrCpy $R7 1
    ${EndIf}

    ${If} $R7 = 0
      Goto freev6_hook_done_${FreeV6HookID}
    ${EndIf}

    ; Native Windows MessageBox — buttons are localized by the OS
    ; (重试/取消 on Chinese Windows). Retry re-runs both checks above.
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "检测到 freev6 仍在运行，无法继续安装。$\n$\n请先退出软件：右键任务栏托盘图标 → 退出。$\n若托盘中没有图标，请先在任务管理器中结束 freev6 进程。$\n退出后点击“重试”继续安装。" /SD IDCANCEL IDRETRY freev6_hook_retry_${FreeV6HookID} IDCANCEL freev6_hook_cancel_${FreeV6HookID}
    Goto freev6_hook_cancel_${FreeV6HookID}

  freev6_hook_cancel_${FreeV6HookID}:
    Abort "已取消安装：请先退出 freev6 后再运行安装程序。"

  freev6_hook_done_${FreeV6HookID}:
  !undef FreeV6HookID
!macroend

; Runs after a silent in-app update (`/S /FREEV6REL`): relaunch the freshly
; installed build with the elevated token this installer inherited — no UAC
; prompt and no second instance (we deliberately do NOT pass /R, whose
; RunAsUser relaunch would come up unelevated and prompt for consent).
; Interactive installs never carry the flag, so this never fires for them.
!macro NSIS_HOOK_POSTINSTALL
  ${GetOptions} $CMDLINE "/FREEV6REL" $R9
  ${IfNot} ${Errors}
    ${If} ${Silent}
      Exec '"$INSTDIR\${MAINBINARYNAME}.exe"'
    ${EndIf}
  ${EndIf}
!macroend
