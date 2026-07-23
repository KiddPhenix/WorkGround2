package work

import "workground2/internal/nilutil"

// ViewSink 消费 WorkViewEvent。Work Service 将传输事件写入此 Sink，
// 由各前端（Desktop、CLI、HTTP/SSE、Bot）订阅消费。
//
// Emit 被串行调用（Work Service 保证同一 Work 的发布有序），
// 实现不应阻塞过久——channel 型 Sink 应有足够缓冲或被活跃消费。
type ViewSink interface {
	EmitWorkView(WorkViewEvent)
}

// ViewFuncSink 将普通函数适配为 ViewSink。
type ViewFuncSink func(WorkViewEvent)

// EmitWorkView 调用包装的函数。
func (f ViewFuncSink) EmitWorkView(e WorkViewEvent) {
	if f != nil {
		f(e)
	}
}

// ViewSinkDiscard 丢弃所有 WorkViewEvent。用于测试和无需 Work 事件的运行时。
var ViewSinkDiscard ViewSink = ViewFuncSink(func(WorkViewEvent) {})

// IsNilViewSink 判断 ViewSink 是否为 nil（含 typed nil）。
func IsNilViewSink(s ViewSink) bool {
	return nilutil.IsNil(s)
}
