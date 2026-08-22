package main

import "workground2/internal/bot"

// ownerDecisionFeatureEnabled 是“主人决策”全局功能的临时屏蔽开关。
//
// 当前为 false（默认关闭）：桌面端不初始化 DecisionBroker、不注册
// /api/v1/decisions/* 远程路由、不接入 IM 决策回复、不投递外部决策，
// 也不自动创建主人完成通知；前端隐藏侧边栏决策中心入口与 Bot 设置的
// 决策通道区域。普通 ask_request 会照常在来源 Session 内显式展示并回答，
// 普通 IM 聊天不受影响。
//
// 恢复：把这里改回 true 即可。持久化的 broker/通道数据未删除，
// 前端通过 ownerDecisionEnabled 字段自动恢复入口与设置项。
const ownerDecisionFeatureEnabled = false

// ownerDecisionActive 返回运行时开关值；nil App 视为关闭。
func (a *App) ownerDecisionActive() bool {
	return a != nil && a.ownerDecisionEnabled
}

// decisionInboundHandler 返回当前应接入 Bot 网关的决策回复处理器。
// 关闭时返回 nil，网关对 /answer 等决策回复不再拦截，普通聊天不受影响。
func (a *App) decisionInboundHandler() func(bot.InboundMessage) (string, bool, error) {
	if !a.ownerDecisionActive() {
		return nil
	}
	return a.handleDecisionInbound
}
