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

; ── 用户协议 / 隐私政策 同意勾选页 ─────────────────────────────────────
; The hooks file is included right after MUI2.nsh and BEFORE every MUI page
; declaration, so this `Page custom` becomes the FIRST wizard page (the stock
; script has no License page — !define LICENSE "" skips MUI_PAGE_LICENSE).
;
; Gating: the Next button starts disabled and only enables when the box is
; ticked; the leave function double-checks (defense in depth). Silent installs
; never call page creators (in-app updates unaffected) and /P passive mode is
; skipped via the template's own SkipIfPassive (forward Call is fine — the
; template does the same for its page PRE functions).
Var F6ConsentChecked

!define F6_AGREEMENT_URL "https://github.com/evansrrr/free-v6/blob/main/docs/agreement.md"
!define F6_PRIVACY_URL "https://github.com/evansrrr/free-v6/blob/main/docs/privacy.md"

Page custom F6ConsentCreate F6ConsentLeave

Function F6ConsentCreate
  Call SkipIfPassive
  StrCpy $F6ConsentChecked 0

  !insertmacro MUI_HEADER_TEXT "用户协议与隐私政策" "请阅读以下条款并勾选同意后继续安装"

  nsDialogs::Create 1018
  Pop $R9
  ${IfThen} $(^RTL) = 1 ${|} nsDialogs::SetRTL $(^RTL) ${|}

  ${NSD_CreateLabel} 0 0 100% 62u "本软件为免费、开源的自由软件，采用 MIT 许可证。完整文本将放置在安装目录下，亦发布于项目仓库 https://github.com/evansrrr/free-v6 "
  Pop $R0

  ${NSD_CreateLabel} 0 74u 100% 10u "在浏览器中查看："
  Pop $R0
  ${NSD_CreateLink} 0 90u 70u 12u "用户协议"
  Pop $R1
  ${NSD_OnClick} $R1 F6OpenAgreement
  ${NSD_CreateLink} 84u 90u 70u 12u "隐私政策"
  Pop $R2
  ${NSD_OnClick} $R2 F6OpenPrivacy

  ${NSD_CreateCheckbox} 0 118u 100% 18u "我已阅读并同意《用户协议》与《隐私政策》"
  Pop $R3
  ${NSD_OnClick} $R3 F6ConsentToggle

  ; Next (control id 1) stays disabled until consent is given
  GetDlgItem $R4 $HWNDPARENT 1
  EnableWindow $R4 0
  ${NSD_SetFocus} $R3

  nsDialogs::Show
FunctionEnd

Function F6ConsentToggle
  Pop $R0
  ${NSD_GetState} $R0 $R1
  ${If} $R1 = ${BST_CHECKED}
    StrCpy $F6ConsentChecked 1
  ${Else}
    StrCpy $F6ConsentChecked 0
  ${EndIf}
  GetDlgItem $R2 $HWNDPARENT 1
  EnableWindow $R2 $F6ConsentChecked
FunctionEnd

Function F6ConsentLeave
  ${If} $F6ConsentChecked != "1"
    MessageBox MB_OK|MB_ICONEXCLAMATION "请先勾选同意《用户协议》与《隐私政策》，然后再继续安装。"
    Abort
  ${EndIf}
FunctionEnd

Function F6OpenAgreement
  Pop $R0
  ExecShell "open" "${F6_AGREEMENT_URL}"
FunctionEnd

Function F6OpenPrivacy
  Pop $R0
  ExecShell "open" "${F6_PRIVACY_URL}"
FunctionEnd
