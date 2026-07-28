# Work 成果卡操作与系统打开：视觉 QA

- Reference: `D:\Temp\codex-clipboard-a31ab160-bec8-40a8-b932-b50773c163b2.png`
- Implementation: `D:\Work\WorkGround2\desktop\build\bin\WorkGround2-artifact-actions.exe`
- Checked: production frontend and Wails bindings compile; status and management controls use separate in-flow regions; generated Blob artifacts expose explicit “打开 / 文件位置” actions.
- Automated visual attempt: launched the independent production executable with Computer Use. The existing `WorkGround2.exe` instance owns the active Work controller, so the QA executable reached “工作暂不可用” and could not render the target artifact card. The existing user window was intentionally left untouched.
- Remaining visual check: restart the primary app with this build, open the completed `预算表.xlsx` Work, and compare the card at normal and narrow widths.

final result: blocked
