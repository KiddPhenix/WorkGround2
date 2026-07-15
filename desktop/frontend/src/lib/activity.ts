// activity — event-driven "internet dark humor" activity copy for the composer status.
//
// Every WireEvent kind maps to one of 16 stages. Each stage has a localized label
// and a weighted pool of flavor phrases (≥8 per locale, non-uniform weights).
// A deterministic seed on stage entry picks one phrase and holds it until the
// stage changes — no 3-second roulette.
//
// Stages
//   waiting_model, planning, executing, thinking, replying,
//   searching, reading, editing, command, testing,
//   processing_result, compacting, waiting_approval,
//   waiting_answer, retrying, tooling

import type { EventKind } from "./types";

// ── Stage type ──────────────────────────────────────────────────────────────

export type Stage =
  | "waiting_model"
  | "planning"
  | "executing"
  | "thinking"
  | "replying"
  | "searching"
  | "reading"
  | "editing"
  | "command"
  | "testing"
  | "processing_result"
  | "compacting"
  | "waiting_approval"
  | "waiting_answer"
  | "retrying"
  | "tooling";

// ── Locale-branded stage labels ─────────────────────────────────────────────

export const STAGE_LABEL: Record<string, Record<Stage, string>> = {
  en: {
    waiting_model: "Waiting for model",
    planning: "Planning",
    executing: "Executing",
    thinking: "Thinking",
    replying: "Replying",
    searching: "Searching",
    reading: "Reading",
    editing: "Editing",
    command: "Running command",
    testing: "Testing",
    processing_result: "Processing result",
    compacting: "Compacting context",
    waiting_approval: "Awaiting approval",
    waiting_answer: "Awaiting answer",
    retrying: "Retrying",
    tooling: "Working",
  },
  zh: {
    waiting_model: "等待模型",
    planning: "规划中",
    executing: "执行中",
    thinking: "思考中",
    replying: "回复中",
    searching: "搜索中",
    reading: "读文件中",
    editing: "改代码",
    command: "执行命令",
    testing: "跑测试",
    processing_result: "处理结果",
    compacting: "整理上下文",
    waiting_approval: "等待确认",
    waiting_answer: "等待答复",
    retrying: "重试中",
    tooling: "工具调用",
  },
  "zh-TW": {
    waiting_model: "等待模型",
    planning: "規劃中",
    executing: "執行中",
    thinking: "思考中",
    replying: "回覆中",
    searching: "搜尋中",
    reading: "讀取檔案",
    editing: "修改程式碼",
    command: "執行命令",
    testing: "執行測試",
    processing_result: "處理結果",
    compacting: "整理上下文",
    waiting_approval: "等待確認",
    waiting_answer: "等待答覆",
    retrying: "重試中",
    tooling: "工具呼叫",
  },
};

// ── Weighted phrase pools ───────────────────────────────────────────────────

interface WeightedEntry {
  text: string;
  weight: number;
}

type StagePool = Record<Stage, WeightedEntry[]>;
type LocalePools = Record<string, StagePool>;

const POOLS: LocalePools = {
  en: {
    waiting_model: [
      { text: "Getting the model into the thread", weight: 5 },
      { text: "Waiting on the first useful token", weight: 4 },
      { text: "Giving compute a moment to join", weight: 3 },
      { text: "The other side is drafting in real time", weight: 4 },
      { text: "Turning latency into suspense", weight: 2 },
      { text: "The request is with the right neurons", weight: 3 },
      { text: "Warming up the context window", weight: 5 },
      { text: "Waiting for upstream to circle back", weight: 2 },
      { text: "The first token is finding a calendar slot", weight: 3 },
      { text: "Getting everyone aligned, including the GPU", weight: 4 },
    ],
    planning: [
      { text: "Turning ambiguity into action items", weight: 5 },
      { text: "Aligning on the shape of the work", weight: 4 },
      { text: "Finding the smallest useful plan", weight: 5 },
      { text: "Giving the roadmap something concrete", weight: 3 },
      { text: "Right-sizing the scope before it grows", weight: 4 },
      { text: "Making the dependencies introduce themselves", weight: 2 },
      { text: "Translating vibes into milestones", weight: 3 },
      { text: "Pressure-testing the happy path", weight: 4 },
      { text: "Writing down what done means", weight: 5 },
      { text: "Making a plan future-us can live with", weight: 3 },
    ],
    executing: [
      { text: "Making the roadmap meet reality", weight: 5 },
      { text: "Turning aligned thinking into actual work", weight: 4 },
      { text: "Moving from strategy to keyboard", weight: 5 },
      { text: "Giving the plan a production environment", weight: 3 },
      { text: "Connecting the dots with commits", weight: 4 },
      { text: "Operationalizing the good idea", weight: 2 },
      { text: "Closing the gap between deck and diff", weight: 4 },
      { text: "Putting the action into action items", weight: 3 },
      { text: "Shipping the part after the meeting", weight: 5 },
      { text: "Making the architecture earn rent", weight: 2 },
    ],
    thinking: [
      { text: "Aligning the dots before connecting them", weight: 4 },
      { text: "Pressure-testing the obvious answer", weight: 5 },
      { text: "Looking for the edge case with opinions", weight: 3 },
      { text: "Turning context into a point of view", weight: 4 },
      { text: "Running a quick internal design review", weight: 5 },
      { text: "Checking whether the shortcut is actually shorter", weight: 3 },
      { text: "Finding the question behind the question", weight: 4 },
      { text: "Giving the trade-offs equal airtime", weight: 2 },
      { text: "Making uncertainty slightly more actionable", weight: 3 },
      { text: "Letting the idea survive contact with context", weight: 5 },
    ],
    replying: [
      { text: "Turning the work into a readable update", weight: 5 },
      { text: "Making the answer Slack-ready", weight: 4 },
      { text: "Converting thoughts into a crisp takeaway", weight: 5 },
      { text: "Removing the meeting that could have been this message", weight: 3 },
      { text: "Putting the conclusion where people can find it", weight: 4 },
      { text: "Right-sizing the level of detail", weight: 3 },
      { text: "Adding just enough context to be useful", weight: 5 },
      { text: "Turning findings into next steps", weight: 4 },
      { text: "Making the response skimmable on purpose", weight: 3 },
      { text: "Drafting something future-us can quote", weight: 2 },
    ],
    searching: [
      { text: "Looking for the source of truth", weight: 5 },
      { text: "Finding the doc behind the doc", weight: 4 },
      { text: "Following the thread with actual evidence", weight: 5 },
      { text: "Searching beyond the first plausible answer", weight: 4 },
      { text: "Checking where the knowledge actually lives", weight: 3 },
      { text: "Turning keywords into useful context", weight: 4 },
      { text: "Looking for the ownerless edge case", weight: 2 },
      { text: "Finding the link everyone remembers vaguely", weight: 3 },
      { text: "Cross-checking the collective memory", weight: 4 },
      { text: "Asking the repository what it knows", weight: 5 },
    ],
    reading: [
      { text: "Getting familiar with the local lore", weight: 4 },
      { text: "Reading the code behind the confidence", weight: 5 },
      { text: "Finding out what the comments left unsaid", weight: 4 },
      { text: "Building context without scheduling a sync", weight: 5 },
      { text: "Checking how the system tells the story", weight: 3 },
      { text: "Getting aligned with the existing logic", weight: 4 },
      { text: "Reading the part the README summarized optimistically", weight: 3 },
      { text: "Looking for the quiet contract in the code", weight: 5 },
      { text: "Understanding why this made sense at the time", weight: 4 },
      { text: "Turning inherited context into current context", weight: 2 },
    ],
    editing: [
      { text: "Turning the plan into a diff", weight: 5 },
      { text: "Making a small change with adult supervision", weight: 3 },
      { text: "Improving the code's future calendar", weight: 2 },
      { text: "Giving the existing logic a clearer next step", weight: 4 },
      { text: "Reducing surprise per line", weight: 5 },
      { text: "Making the happy path easier to explain", weight: 4 },
      { text: "Updating the code without starting a reorg", weight: 3 },
      { text: "Paying down one carefully scoped unit of debt", weight: 5 },
      { text: "Making future debugging slightly less cinematic", weight: 4 },
      { text: "Moving the implementation toward the agreed reality", weight: 3 },
    ],
    command: [
      { text: "Giving the terminal an action item", weight: 5 },
      { text: "Letting the CLI weigh in", weight: 4 },
      { text: "Running the part that cannot fit in a status update", weight: 3 },
      { text: "Asking the system for a concrete answer", weight: 5 },
      { text: "Turning keyboard intent into process output", weight: 4 },
      { text: "Giving automation a chance to look competent", weight: 2 },
      { text: "Checking whether the environment agrees", weight: 5 },
      { text: "Inviting the command line into the loop", weight: 3 },
      { text: "Running the workflow's practical portion", weight: 4 },
      { text: "Letting stdout provide the meeting notes", weight: 3 },
    ],
    testing: [
      { text: "Pressure-testing the happy path", weight: 5 },
      { text: "Asking the edge cases for feedback", weight: 4 },
      { text: "Checking whether confidence compiles", weight: 5 },
      { text: "Letting the test suite join the review", weight: 4 },
      { text: "Validating the part the demo skips", weight: 3 },
      { text: "Looking for feedback before production does", weight: 5 },
      { text: "Making the assumptions show their work", weight: 4 },
      { text: "Checking if green means actually green", weight: 3 },
      { text: "Giving the change a structured disagreement", weight: 2 },
      { text: "Turning optimism into evidence", weight: 5 },
    ],
    processing_result: [
      { text: "Turning output into a useful takeaway", weight: 5 },
      { text: "Checking what the system actually said", weight: 5 },
      { text: "Separating signal from very enthusiastic noise", weight: 3 },
      { text: "Converting logs into next steps", weight: 4 },
      { text: "Closing the loop with evidence", weight: 5 },
      { text: "Reading past the successful exit code", weight: 3 },
      { text: "Making the result decision-ready", weight: 4 },
      { text: "Checking whether the output supports the narrative", weight: 2 },
      { text: "Finding the one line that changes the plan", weight: 4 },
      { text: "Turning machine feedback into human context", weight: 5 },
    ],
    compacting: [
      { text: "Right-sizing the context window", weight: 5 },
      { text: "Turning the thread into a useful brief", weight: 4 },
      { text: "Making room without losing the plot", weight: 5 },
      { text: "Compressing the meeting into action items", weight: 3 },
      { text: "Keeping the signal and trimming the scrollback", weight: 4 },
      { text: "Giving the context a cleaner handoff", weight: 5 },
      { text: "Consolidating what future-us actually needs", weight: 4 },
      { text: "Reducing history to decision-grade notes", weight: 3 },
      { text: "Making the next turn less archaeological", weight: 2 },
      { text: "Tidying the thread before it becomes a channel", weight: 4 },
    ],
    waiting_approval: [
      { text: "Ready when you give the go-ahead", weight: 5 },
      { text: "Holding at the human checkpoint", weight: 4 },
      { text: "The next move is yours", weight: 5 },
      { text: "Waiting for a clear yes or no", weight: 4 },
      { text: "Keeping the decision with the right person", weight: 3 },
      { text: "The action is ready for your review", weight: 5 },
      { text: "Paused at the permission boundary", weight: 4 },
      { text: "Standing by for approval", weight: 3 },
      { text: "No surprises past this point", weight: 2 },
      { text: "Ready to proceed on your signal", weight: 4 },
    ],
    waiting_answer: [
      { text: "One input away from moving forward", weight: 5 },
      { text: "The floor is yours", weight: 4 },
      { text: "Waiting on the context only you have", weight: 5 },
      { text: "Ready for your call", weight: 4 },
      { text: "The options are lined up", weight: 3 },
      { text: "Pausing the AI improv", weight: 2 },
      { text: "A small decision with useful consequences", weight: 4 },
      { text: "Waiting for the missing piece", weight: 5 },
      { text: "Your context closes the gap", weight: 3 },
      { text: "Ready when the answer is", weight: 2 },
    ],
    retrying: [
      { text: "Giving upstream another moment", weight: 5 },
      { text: "Re-opening the conversation", weight: 4 },
      { text: "The network is circling back", weight: 3 },
      { text: "Trying the same request with better timing", weight: 5 },
      { text: "Letting the connection recover gracefully", weight: 4 },
      { text: "Rejoining the thread", weight: 3 },
      { text: "Taking one more run at the handoff", weight: 4 },
      { text: "Waiting for the service to be ready-ready", weight: 2 },
      { text: "Resending without making it a meeting", weight: 3 },
      { text: "Applying a small amount of operational optimism", weight: 5 },
    ],
    tooling: [
      { text: "Bringing the right tool into the thread", weight: 5 },
      { text: "Delegating to specialized software", weight: 4 },
      { text: "Giving the workflow an extra pair of hands", weight: 3 },
      { text: "Using the feature built for exactly this", weight: 5 },
      { text: "Letting the tool do the tool-shaped work", weight: 4 },
      { text: "Connecting the task to the right capability", weight: 5 },
      { text: "Moving this out of manual mode", weight: 3 },
      { text: "Adding one practical dependency to the loop", weight: 2 },
      { text: "Calling in the specialist", weight: 4 },
      { text: "Turning intent into an actual operation", weight: 5 },
    ],
  },

  zh: {
    waiting_model: [
      { text: "暖个炉", weight: 3 },
      { text: "叫模型起床", weight: 4 },
      { text: "从虚空抓取 Token", weight: 5 },
      { text: "加载 brain.exe 中", weight: 4 },
      { text: "启动推理引擎", weight: 3 },
      { text: "让 GPU 出列", weight: 3 },
      { text: "冲泡认知咖啡", weight: 2 },
      { text: "给概率气球打气", weight: 2 },
      { text: "喂神经网络仓鼠", weight: 4 },
      { text: "召唤硅基先知", weight: 3 },
    ],
    planning: [
      { text: "先把饼画圆", weight: 5 },
      { text: "对齐颗粒度", weight: 4 },
      { text: "测算这个坑有多深", weight: 4 },
      { text: "给技术债修边", weight: 3 },
      { text: "和未来的自己谈判", weight: 3 },
      { text: "搭建甩锅脚手架", weight: 5 },
      { text: "把任务拆成可承受的 panic", weight: 4 },
      { text: "准备过度设计的架构图", weight: 2 },
      { text: "数数这次要动多少文件", weight: 3 },
      { text: "拉通对齐本次迭代的顶层设计", weight: 4 },
    ],
    executing: [
      { text: "把方案推进落地", weight: 5 },
      { text: "PPT 开始长出代码", weight: 4 },
      { text: "打通传说中的最后一公里", weight: 3 },
      { text: "让计划接受现实检验", weight: 5 },
      { text: "从方法论切换到体力活", weight: 4 },
      { text: "把抓手接到执行链路", weight: 2 },
      { text: "推动交付闭环自然发生", weight: 3 },
      { text: "进入真正干活的环节", weight: 5 },
      { text: "正在落地已经对齐的对齐", weight: 2 },
      { text: "让架构图开始产生价值", weight: 4 },
    ],
    thinking: [
      { text: "连一下决策树", weight: 4 },
      { text: "让梯度下降", weight: 4 },
      { text: "和橡皮鸭聊人生", weight: 5 },
      { text: "在依赖图里散步", weight: 3 },
      { text: "称量 tradeoff 的宇宙重量", weight: 2 },
      { text: "连接没人要求连接的 dots", weight: 4 },
      { text: "模拟 merge conflict 多重宇宙", weight: 3 },
      { text: "计算贝叶斯愧疚指数", weight: 4 },
      { text: "思考这改动的存在意义", weight: 3 },
      { text: "和虚空交换意见", weight: 2 },
    ],
    replying: [
      { text: "打磨完美免责话术", weight: 4 },
      { text: "以最少确定性输出最大自信", weight: 5 },
      { text: "用 Markdown 传递灵魂", weight: 2 },
      { text: "抛光相关性的外皮", weight: 3 },
      { text: "吐 token 如同吐珍珠", weight: 4 },
      { text: "说得好像你想出来的", weight: 3 },
      { text: "算出最佳啰嗦程度", weight: 2 },
      { text: "起草最不引火上身的回答", weight: 5 },
      { text: "确保至少有一个类比", weight: 2 },
      { text: "编造一个合理的故事", weight: 4 },
    ],
    searching: [
      { text: "用高科技玩 grep", weight: 4 },
      { text: "考古祖传逻辑", weight: 5 },
      { text: "追踪幽灵依赖", weight: 3 },
      { text: "和老代码玩捉迷藏", weight: 2 },
      { text: "进入遗留系统的迷宫", weight: 4 },
      { text: "在技术债堆里捞针", weight: 5 },
      { text: "调查上一个案发现场", weight: 3 },
      { text: "对账未文档化的行为", weight: 4 },
      { text: "跟随前人绝望的足迹", weight: 3 },
      { text: "读文档是为了不让你读", weight: 2 },
    ],
    reading: [
      { text: "考古祖传逻辑", weight: 5 },
      { text: "解读意大利面条经文", weight: 4 },
      { text: "跟踪穿越抽象层的数据流", weight: 3 },
      { text: "读出 README 的弦外之音", weight: 2 },
      { text: "用 git blame 找答案", weight: 5 },
      { text: "浏览真相和谎言的源代码", weight: 3 },
      { text: "把五年前的注释翻译回现实", weight: 4 },
      { text: "理解为什么它用这种方式™", weight: 4 },
      { text: "领会上一轮迭代的聪明", weight: 3 },
      { text: "在脑子里补全缺失的类型注解", weight: 2 },
    ],
    editing: [
      { text: "给技术债续命", weight: 5 },
      { text: "插入恰到好处的 regret", weight: 3 },
      { text: "把意大利面揉成千层面", weight: 4 },
      { text: "重构昨天的罪过", weight: 5 },
      { text: "让 linter 少生一点气", weight: 3 },
      { text: "再加一个间接层", weight: 2 },
      { text: "生成你应得的模板代码", weight: 4 },
      { text: "书写命运的 diff", weight: 3 },
      { text: "注入今日份魔法常量", weight: 2 },
      { text: "调到测试通过或放弃治疗", weight: 5 },
    ],
    command: [
      { text: "执行咒语", weight: 3 },
      { text: "按回车且祈求好运", weight: 5 },
      { text: "运行那个绝对不会有问题的小脚本", weight: 4 },
      { text: "观看命运的进度条", weight: 3 },
      { text: "让 CLI 决定我们的命运", weight: 4 },
      { text: "触发连锁副作用", weight: 2 },
      { text: "用可疑权限 spawn 进程", weight: 3 },
      { text: "执行上次能用的命令™", weight: 5 },
      { text: "把 stdout 输向虚空", weight: 2 },
      { text: "执行古老的 apt-get 仪式", weight: 3 },
    ],
    testing: [
      { text: "让 Bug 主动交代", weight: 5 },
      { text: "看着绿色点点铺满屏幕", weight: 3 },
      { text: "看看测试套件如何评价我们", weight: 4 },
      { text: "向覆盖率之神祈祷", weight: 5 },
      { text: "向边缘 case 宣告主权", weight: 3 },
      { text: "跑 CI 的荆棘之路", weight: 4 },
      { text: "让 Jest 决定我们值不值得", weight: 3 },
      { text: "用 flaky test 试探命运", weight: 4 },
      { text: "等待红变绿的奇迹", weight: 5 },
      { text: "发现这次又猜错了什么", weight: 3 },
    ],
    processing_result: [
      { text: "确认是谁的锅", weight: 5 },
      { text: "解析生活的返回码", weight: 3 },
      { text: "搞清楚刚才到底发生了什么", weight: 5 },
      { text: "把 stderr 翻译回中文", weight: 2 },
      { text: "为甩锅会收集证据", weight: 4 },
      { text: "把 chaos 摘要成能用的 nugget", weight: 4 },
      { text: "从噪声中提取有效信号", weight: 3 },
      { text: "决定是报喜还是藏尸", weight: 4 },
      { text: "计算产出投入比", weight: 2 },
      { text: "准备好报喜不报忧的面具", weight: 3 },
    ],
    compacting: [
      { text: "删除不利记忆", weight: 5 },
      { text: "归档我们宁愿忘掉的部分", weight: 4 },
      { text: "把对话压缩成不具指认性的摘要", weight: 5 },
      { text: "修剪 token 花园", weight: 2 },
      { text: "总结到目前为止的剧情", weight: 3 },
      { text: "把上下文压成一个可控创伤", weight: 4 },
      { text: "隐去尴尬的早期尝试", weight: 5 },
      { text: "为下一轮制作 highlight 集锦", weight: 3 },
      { text: "烧聊天记录（比喻意义上的）", weight: 4 },
      { text: "碎片化不利于结论的证据", weight: 4 },
    ],
    waiting_approval: [
      { text: "等待您的审批手势", weight: 3 },
      { text: "端着 diff 等您过目", weight: 4 },
      { text: "铺好红毯等您拍板", weight: 2 },
      { text: "停在人类审批门前", weight: 4 },
      { text: "等一个赞或踩的信号", weight: 3 },
      { text: "在您决定前保持冻结", weight: 2 },
      { text: "请求开火许可中", weight: 5 },
      { text: "等待指挥中心的绿灯", weight: 3 },
      { text: "按住不动 — 人在回路", weight: 4 },
      { text: "您一声令下就继续", weight: 2 },
    ],
    waiting_answer: [
      { text: "等您的高见", weight: 3 },
      { text: "期待您的高明输入", weight: 4 },
      { text: "轮到您了 — 舞台交给您", weight: 3 },
      { text: "您思考时我也在思考", weight: 2 },
      { text: "等您做决定", weight: 4 },
      { text: "双手奉上问题", weight: 3 },
      { text: "您准备好了我随时", weight: 5 },
      { text: "洗耳恭听您的回答", weight: 2 },
      { text: "等您下令", weight: 3 },
      { text: "掰手指等您决定", weight: 2 },
    ],
    retrying: [
      { text: "拍拍灰再来一次", weight: 5 },
      { text: "第二回合 — 带感情", weight: 4 },
      { text: "施展重启大法", weight: 3 },
      { text: "换几个参数再试一次", weight: 4 },
      { text: "从错误中学习（大概吧）", weight: 2 },
      { text: "再试一下大学时代的热情", weight: 3 },
      { text: "做同样的事期待不同结果", weight: 5 },
      { text: "加载备份计划（没有备份计划）", weight: 4 },
      { text: "重做刚才的事，但更暴躁", weight: 3 },
      { text: "点重试因为希望永存", weight: 4 },
    ],
    tooling: [
      { text: "从工具箱掏出趁手的家伙", weight: 3 },
      { text: "调用合适的工具函数", weight: 2 },
      { text: "掏出瑞士军刀", weight: 4 },
      { text: "调用一个大概率存在的函数", weight: 5 },
      { text: "用手上的工具执行计划", weight: 3 },
      { text: "给问题选对工具", weight: 2 },
      { text: "用正确比例的力解决问题", weight: 4 },
      { text: "发挥协同增效能力", weight: 3 },
      { text: "热情地操作工具栈", weight: 2 },
      { text: "把 '工具' 放进 '工具辅助编码'", weight: 4 },
    ],
  },

  "zh-TW": {
    waiting_model: [
      { text: "暖個爐", weight: 3 },
      { text: "叫模型起床", weight: 4 },
      { text: "從虛空抓取 Token", weight: 5 },
      { text: "載入 brain.exe 中", weight: 4 },
      { text: "啟動推理引擎", weight: 3 },
      { text: "讓 GPU 出列", weight: 3 },
      { text: "沖泡認知咖啡", weight: 2 },
      { text: "給氣球打氣", weight: 2 },
      { text: "餵神經網路倉鼠", weight: 4 },
      { text: "召喚矽基先知", weight: 3 },
    ],
    planning: [
      { text: "先把餅畫圓", weight: 5 },
      { text: "對齊顆粒度", weight: 4 },
      { text: "測算這個坑有多深", weight: 4 },
      { text: "給技術債修邊", weight: 3 },
      { text: "和未來的自己談判", weight: 3 },
      { text: "搭建甩鍋鷹架", weight: 5 },
      { text: "把任務拆成可承受的 panic", weight: 4 },
      { text: "準備過度設計的架構圖", weight: 2 },
      { text: "數數這次要動多少檔案", weight: 3 },
      { text: "拉通對齊本次迭代的頂層設計", weight: 4 },
    ],
    executing: [
      { text: "把方案推進落地", weight: 5 },
      { text: "PPT 開始長出程式碼", weight: 4 },
      { text: "打通傳說中的最後一公里", weight: 3 },
      { text: "讓計畫接受現實檢驗", weight: 5 },
      { text: "從方法論切換到體力活", weight: 4 },
      { text: "把抓手接到執行鏈路", weight: 2 },
      { text: "推動交付閉環自然發生", weight: 3 },
      { text: "進入真正幹活的環節", weight: 5 },
      { text: "正在落地已經對齊的對齊", weight: 2 },
      { text: "讓架構圖開始產生價值", weight: 4 },
    ],
    thinking: [
      { text: "連一下決策樹", weight: 4 },
      { text: "讓梯度下降", weight: 4 },
      { text: "和橡皮鴨聊人生", weight: 5 },
      { text: "在依賴圖裡散步", weight: 3 },
      { text: "衡量 tradeoff 的宇宙重量", weight: 2 },
      { text: "連接沒人要求連接的 dots", weight: 4 },
      { text: "模擬 merge conflict 多重宇宙", weight: 3 },
      { text: "計算貝氏愧疚指數", weight: 4 },
      { text: "思考這改動的存在意義", weight: 3 },
      { text: "和虛空交換意見", weight: 2 },
    ],
    replying: [
      { text: "打磨完美免責話術", weight: 4 },
      { text: "以最少確定性輸出最大自信", weight: 5 },
      { text: "用 Markdown 傳遞靈魂", weight: 2 },
      { text: "拋光相關性外皮", weight: 3 },
      { text: "吐 token 如吐珍珠", weight: 4 },
      { text: "說得像你想出來的一樣", weight: 3 },
      { text: "算出最佳囉嗦程度", weight: 2 },
      { text: "起草最不引火上身的回答", weight: 5 },
      { text: "確保至少有一個類比", weight: 2 },
      { text: "編造一個合理的故事", weight: 4 },
    ],
    searching: [
      { text: "用高科技玩 grep", weight: 4 },
      { text: "考古祖傳邏輯", weight: 5 },
      { text: "追蹤幽靈依賴", weight: 3 },
      { text: "和老程式玩捉迷藏", weight: 2 },
      { text: "進入遺留系統的迷宮", weight: 4 },
      { text: "在技術債堆裡撈針", weight: 5 },
      { text: "調查上一個案發現場", weight: 3 },
      { text: "對帳未文件化的行為", weight: 4 },
      { text: "跟隨前人絕望的足跡", weight: 3 },
      { text: "讀文件是為了不讓你讀", weight: 2 },
    ],
    reading: [
      { text: "考古祖傳邏輯", weight: 5 },
      { text: "解讀義大利麵經文", weight: 4 },
      { text: "追蹤穿越抽象層的資料流", weight: 3 },
      { text: "讀出 README 的弦外之音", weight: 2 },
      { text: "用 git blame 找答案", weight: 5 },
      { text: "瀏覽真相和謊言的原始碼", weight: 3 },
      { text: "把五年前的註解翻譯回現實", weight: 4 },
      { text: "理解為什麼它用這種方式™", weight: 4 },
      { text: "領會上一輪迭代的聰明", weight: 3 },
      { text: "在腦子裡補全缺失的型別註解", weight: 2 },
    ],
    editing: [
      { text: "給技術債續命", weight: 5 },
      { text: "插入恰到好處的 regret", weight: 3 },
      { text: "把義大利麵揉成千層麵", weight: 4 },
      { text: "重構昨天的罪過", weight: 5 },
      { text: "讓 linter 少生一點氣", weight: 3 },
      { text: "再加一個間接層", weight: 2 },
      { text: "生成你應得的樣板程式碼", weight: 4 },
      { text: "書寫命運的 diff", weight: 3 },
      { text: "注入今日份魔法常數", weight: 2 },
      { text: "調到測試通過或放棄治療", weight: 5 },
    ],
    command: [
      { text: "執行咒語", weight: 3 },
      { text: "按 Enter 且祈求好運", weight: 5 },
      { text: "執行那個絕對不會有問題的小腳本", weight: 4 },
      { text: "觀看命運的進度條", weight: 3 },
      { text: "讓 CLI 決定我們的命運", weight: 4 },
      { text: "觸發連鎖副作用", weight: 2 },
      { text: "用可疑權限建立行程", weight: 3 },
      { text: "執行上次能用的命令™", weight: 5 },
      { text: "把 stdout 輸向虛空", weight: 2 },
      { text: "執行古老的 apt-get 儀式", weight: 3 },
    ],
    testing: [
      { text: "讓 Bug 主動交代", weight: 5 },
      { text: "看著綠色點點鋪滿螢幕", weight: 3 },
      { text: "看看測試套件如何評價我們", weight: 4 },
      { text: "向覆蓋率之神祈禱", weight: 5 },
      { text: "向邊界案例宣告主權", weight: 3 },
      { text: "跑 CI 的荊棘之路", weight: 4 },
      { text: "讓 Jest 決定我們值不值得", weight: 3 },
      { text: "試探命運的 flaky test", weight: 4 },
      { text: "等待紅變綠的奇蹟", weight: 5 },
      { text: "發現這次又猜錯了什麼", weight: 3 },
    ],
    processing_result: [
      { text: "確認是誰的鍋", weight: 5 },
      { text: "解析生活的回傳碼", weight: 3 },
      { text: "搞清楚剛才到底發生了什麼", weight: 5 },
      { text: "把 stderr 翻譯回中文", weight: 2 },
      { text: "為甩鍋會議收集證據", weight: 4 },
      { text: "把 chaos 摘要成能用的 nugget", weight: 4 },
      { text: "從雜訊中提取有效信號", weight: 3 },
      { text: "決定是報喜還是藏屍", weight: 4 },
      { text: "計算產出投入比", weight: 2 },
      { text: "準備好報喜不報憂的面具", weight: 3 },
    ],
    compacting: [
      { text: "刪除不利記憶", weight: 5 },
      { text: "歸檔我們寧願忘掉的部分", weight: 4 },
      { text: "把對話壓縮成不具指認性的摘要", weight: 5 },
      { text: "修剪 token 花園", weight: 2 },
      { text: "總結到目前為止的劇情", weight: 3 },
      { text: "把上下文壓成一個可控創傷", weight: 4 },
      { text: "隱去尷尬的早期嘗試", weight: 5 },
      { text: "為下一輪製作 highlight 精選", weight: 3 },
      { text: "焚燒聊天記錄（比喻意義上的）", weight: 4 },
      { text: "碎片化不利於結論的證據", weight: 4 },
    ],
    waiting_approval: [
      { text: "等待您的審批手勢", weight: 3 },
      { text: "端著 diff 等您過目", weight: 4 },
      { text: "鋪好紅毯等您拍板", weight: 2 },
      { text: "停在人類審批門前", weight: 4 },
      { text: "等一個讚或踩的信號", weight: 3 },
      { text: "在您決定前保持凍結", weight: 2 },
      { text: "請求開火許可中", weight: 5 },
      { text: "等待指揮中心的綠燈", weight: 3 },
      { text: "按住不動 — 人在迴路", weight: 4 },
      { text: "您一聲令下就繼續", weight: 2 },
    ],
    waiting_answer: [
      { text: "等您的高見", weight: 3 },
      { text: "期待您的高明輸入", weight: 4 },
      { text: "輪到您了 — 舞台交給您", weight: 3 },
      { text: "您思考時我也在思考", weight: 2 },
      { text: "等您做決定", weight: 4 },
      { text: "雙手奉上問題", weight: 3 },
      { text: "您準備好了我隨時", weight: 5 },
      { text: "洗耳恭聽您的回答", weight: 2 },
      { text: "等您下令", weight: 3 },
      { text: "掰手指等您決定", weight: 2 },
    ],
    retrying: [
      { text: "拍拍灰塵再來一次", weight: 5 },
      { text: "第二回合 — 帶感情", weight: 4 },
      { text: "施展重啟大法", weight: 3 },
      { text: "換幾個參數再試一次", weight: 4 },
      { text: "從錯誤中學習（大概吧）", weight: 2 },
      { text: "再試一次大學時代的熱情", weight: 3 },
      { text: "做同樣的事期待不同結果", weight: 5 },
      { text: "載入備份計畫（沒有備份計畫）", weight: 4 },
      { text: "重做剛才的事，但更暴躁", weight: 3 },
      { text: "點重試因為希望永存", weight: 4 },
    ],
    tooling: [
      { text: "從工具箱掏出趁手的傢伙", weight: 3 },
      { text: "呼叫合適的函式", weight: 2 },
      { text: "掏出瑞士刀", weight: 4 },
      { text: "呼叫一個大概率存在的函式", weight: 5 },
      { text: "用手上的工具執行計畫", weight: 3 },
      { text: "給問題選對工具", weight: 2 },
      { text: "用正確比例的力量解決問題", weight: 4 },
      { text: "發揮協同增效能力", weight: 3 },
      { text: "熱情地操作工具棧", weight: 2 },
      { text: "把『工具』放進『工具輔助編碼』", weight: 4 },
    ],
  },
};

// ── Deterministic weighted picker ───────────────────────────────────────────

/** Simple mulberry32 PRNG from a 32-bit seed. */
function mulberry32(seed: number): () => number {
  let s = seed | 0;
  return () => {
    s = (s + 0x6d2b79f5) | 0;
    let t = Math.imul(s ^ (s >>> 15), 1 | s);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/**
 * Pick an entry from a weighted pool using a deterministic seed.
 * Returns `{ text, index }` so callers can cache and reuse the same
 * pick for the duration of a stage.
 */
export function pickWeighted(
  pool: WeightedEntry[],
  seed: number,
): { text: string; index: number } {
  if (pool.length === 0) return { text: "", index: -1 };

  const total = pool.reduce((sum, e) => sum + e.weight, 0);
  if (total <= 0) return { text: pool[0].text, index: 0 };

  const rng = mulberry32(seed);
  const threshold = rng() * total;
  let cumulative = 0;

  for (let i = 0; i < pool.length; i++) {
    cumulative += pool[i].weight;
    if (threshold < cumulative) {
      return { text: pool[i].text, index: i };
    }
  }

  // Fallback (shouldn't happen due to floating point epsilon)
  return { text: pool[pool.length - 1].text, index: pool.length - 1 };
}

// ── Tool → stage classification ─────────────────────────────────────────────

// Recognizable test-command prefixes for shell tool classification.
const TEST_COMMAND_PREFIXES = [
  "go test",
  "npm test",
  "npm.cmd test",
  "npm run test",
  "npm.cmd run test",
  "npx tsx",
  "npx jest",
  "npx vitest",
  "npx mocha",
  "node --test",
  "cargo test",
  "cargo nextest",
  "mvn test",
  "mvn verify",
  "pytest",
  "python -m pytest",
  "python3 -m pytest",
  "make test",
  "rake test",
  "dotnet test",
  "nuget test",
];

function isTestCommand(args: string): boolean {
  let command = args;
  try {
    const parsed = JSON.parse(args) as { command?: unknown; cmd?: unknown };
    const candidate = typeof parsed.command === "string" ? parsed.command : parsed.cmd;
    if (typeof candidate === "string") command = candidate;
  } catch {
    // Some frontends already pass the extracted command rather than tool JSON.
  }
  const normalized = command.trim().toLowerCase();
  return TEST_COMMAND_PREFIXES.some((prefix) =>
    normalized === prefix ||
    normalized.startsWith(`${prefix} `) ||
    normalized.includes(`; ${prefix}`) ||
    normalized.includes(`&& ${prefix}`) ||
    normalized.includes(`| ${prefix}`),
  );
}

/**
 * Classify a tool dispatch into an activity stage.
 * Exported for testing.
 */
export function classifyTool(
  name: string,
  args: string,
): Extract<Stage, "searching" | "reading" | "editing" | "testing" | "command" | "tooling"> {
  switch (name) {
    // Searching
    case "grep":
    case "code_index":
    case "research":
    case "explore":
      return "searching";

    // Reading
    case "read_file":
    case "ls":
    case "glob":
    case "web_fetch":
    case "read_skill":
    case "read_only_skill":
    case "read_only_task":
    case "bash_output":
    case "waitJob":
    case "todo_write":
    case "memory":
    case "lsp_diagnostics":
    case "lsp_hover":
    case "lsp_definition":
    case "lsp_references":
      return "reading";

    // Editing
    case "edit_file":
    case "write_file":
    case "multi_edit":
    case "delete_range":
    case "delete_symbol":
    case "move_file":
    case "notebook_edit":
      return "editing";

    // Shell
    case "bash":
    case "shell":
    case "pwsh":
    case "cmd":
    case "run":
      return isTestCommand(args) ? "testing" : "command";

    default:
      return "tooling";
  }
}

// ── Event → stage mapping ───────────────────────────────────────────────────

// Phase-text patterns that suggest planning mode.
const PLAN_PATTERNS = /(?:^(?:规划|規劃|方案)|(?:^|[·\s])(?:plan|planning|approach|architecture)(?:$|[·\s]))/i;
const EXECUTE_PATTERNS = /(?:^(?:执行|執行)|(?:^|[·\s])(?:execute|executing)(?:$|[·\s]))/i;

/**
 * Map a WireEvent kind and its payload to an activity stage.
 * Returns null when the event should clear the stage (turn_done).
 * Returns undefined when the event should not change the current stage.
 */
export function stageFromEvent(
  kind: EventKind,
  text?: string,
  toolName?: string,
  toolArgs?: string,
  source?: string,
): Stage | null | undefined {
  switch (kind) {
    case "turn_started":
      return "waiting_model";

    case "reasoning":
      return "thinking";

    case "text":
    case "message":
      return "replying";

    case "phase":
      if (source === "planner") return "planning";
      if (source === "executor") return "executing";
      if (text && PLAN_PATTERNS.test(text)) return "planning";
      return text && EXECUTE_PATTERNS.test(text) ? "executing" : undefined;

    case "tool_dispatch":
      if (!toolName) return undefined;
      return classifyTool(toolName, toolArgs ?? "");

    case "tool_result":
      return "processing_result";

    case "compaction_started":
    case "compaction_done":
      return "compacting";

    case "approval_request":
      return "waiting_approval";

    case "ask_request":
      return "waiting_answer";

    case "retrying":
      return "retrying";

    case "turn_done":
      return null;

    default:
      return undefined;
  }
}

// ── High-level helpers for the composer ──────────────────────────────────────

export type StagePick = {
  stage: Stage;
  label: string;
  flavor: string;
  seed: number;
};

/**
 * Compute the display text for a stage and locale.
 * The seed is derived from the stage name + epoch-seconds at entry, giving
 * a stable pick per stage session.
 */
export function stageDisplay(
  stage: Stage,
  locale: string,
  stageEntrySeed: number,
): { label: string; flavor: string } {
  const labels = STAGE_LABEL[locale] ?? STAGE_LABEL.en;
  const pools = POOLS[locale] ?? POOLS.en;
  const label = labels[stage];
  const pool = pools[stage];
  const { text } = pickWeighted(pool, stageEntrySeed);
  return { label, flavor: text };
}

/** Return the concise flavor copy used by the compact composer status pill. */
export function activityLead(flavor: string): string {
  return flavor;
}

/** Return all weighted entries for a stage+locale (for testing). */
export function getPool(
  stage: Stage,
  locale: string,
): WeightedEntry[] {
  return (POOLS[locale] ?? POOLS.en)[stage];
}

/** Return all stage labels for a locale (for testing). */
export function getStageLabels(locale: string): Record<Stage, string> {
  return STAGE_LABEL[locale] ?? STAGE_LABEL.en;
}
