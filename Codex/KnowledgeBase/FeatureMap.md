# Feature Map

| 功能名 | 状态 | 分支 | 负责人 | 主要文件 | 备注 |
|---|---|---|---|---|---|
| 会话总结浮层显示全文 | done | `developping/full-session-summary-tooltip+2026-07-12` | Codex | `desktop/frontend/src/components/desktop-ui/IrisInfoComponents.tsx`, `desktop/frontend/src/__tests__/desktop-ui-components.test.tsx` | 保留总结清洗后的完整文本，行内继续由 CSS 省略，悬浮浮层展示全文。149 项专项组件测试、TypeScript 检查和前端生产构建通过。 |
| 桌面通用设置精简 | done | `developping/general-settings-cleanup+2026-07-12` | Codex | `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/frontend/src/locales`, `desktop/frontend/src/__tests__/settings-refresh-snapshot.test.tsx` | 已隐藏桌面风格、会话展示模式和底部信息栏配置；工作台保持默认且旧配置继续兼容；新会话审批改为“需要批准 / 自动批准 / 全部允许”并提供悬浮说明。类型检查、18 项专项回归、前端生产构建和配置默认值测试通过。 |
| 隐藏崩溃报告发送按钮 | done | `developping/hide-crash-report-send+2026-07-11` | Codex | `desktop/frontend/src/lib/crash.ts` | 崩溃弹窗仅保留复制；性能诊断上报入口不受影响。TypeScript 检查及崩溃上报测试通过。 |
