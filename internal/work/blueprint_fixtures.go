package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ── Five-scenario V2 Blueprint fixtures ─────────────────────────────────────
//
// Each scenario is a system built-in WorkBlueprint paired with a companion
// V2 WorkDefinitionRevision template. The Blueprint is registered in the
// BlueprintRegistry so ListBlueprints and Create work through the production
// controller path. The V2 definition template carries the scenario-specific
// NodeDef DAG, ArtifactSlotDef declarations, and typed InputSpec gates.
//
// Production flow:
//   Controller → Service.Create (BlueprintRef) → BeginWorkPlanning →
//   CreateCandidateRevision (V2 template) → ApplyDefinition →
//   V2Coordinator → V2Scheduler → TaskExecutor
//
// All five scenarios share the same execution skeleton — only artifact slots,
// nodes, typed inputs, and approval gates differ.

// ── Common V2 definition helpers ────────────────────────────────────────────

// V2DefinitionTemplate maps a Blueprint ID to its V2 WorkDefinitionRevision
// template. Tests use this to create candidate revisions through the production
// CreateCandidateRevision path.
func V2DefinitionTemplate(bpID string) *WorkDefinitionRevision {
	switch bpID {
	case "blueprint:image-compile":
		return v2DefImageCompile()
	case "blueprint:script-writing":
		return v2DefScriptWriting()
	case "blueprint:financial-budget":
		return v2DefFinancialBudget()
	case "blueprint:git-release":
		return v2DefGitRelease()
	case "blueprint:annual-event":
		return v2DefAnnualEvent()
	default:
		return nil
	}
}

// V2BlueprintIDs returns all five V2 scenario blueprint IDs.
func V2BlueprintIDs() []string {
	return []string{
		"blueprint:image-compile",
		"blueprint:script-writing",
		"blueprint:financial-budget",
		"blueprint:git-release",
		"blueprint:annual-event",
	}
}

// BeginBlueprintPlanningInput selects a built-in V2 Blueprint through the
// production planning service. RequestID identifies the whole multi-step
// intent; the service derives stable child request IDs for begin, candidate,
// and apply so a partial failure can be retried safely.
type BeginBlueprintPlanningInput struct {
	BlueprintID string `json:"blueprintId"`
	SessionID   string `json:"sessionId"`
	RequestID   string `json:"requestId"`
}

// BeginBlueprintPlanningResult reports the authoritative Work and Definition
// selected by BeginBlueprintPlanning.
type BeginBlueprintPlanningResult struct {
	BlueprintRef       BlueprintRef           `json:"blueprintRef"`
	WorkID             string                 `json:"workId"`
	DefinitionRevision int64                  `json:"definitionRevision"`
	Apply              *ApplyDefinitionResult `json:"apply,omitempty"`
}

// BeginBlueprintPlanning creates, defines, and applies one of the built-in V2
// Blueprint fixtures through the same Service, FileWorkStore, and V2Scheduler
// path used by production Controllers.
func (s *Service) BeginBlueprintPlanning(ctx context.Context, input BeginBlueprintPlanningInput) (*BeginBlueprintPlanningResult, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.blueprint == nil {
		return nil, errors.New("work: BeginBlueprintPlanning: blueprint registry is unavailable")
	}
	blueprintID := strings.TrimSpace(input.BlueprintID)
	if blueprintID == "" {
		return nil, errors.New("work: BeginBlueprintPlanning: blueprintId is required")
	}
	requestID, err := requireRequestID("BeginBlueprintPlanning", input.RequestID)
	if err != nil {
		return nil, err
	}
	bp, err := s.blueprint.LookupLatest(blueprintID)
	if err != nil {
		return nil, fmt.Errorf("work: BeginBlueprintPlanning: lookup %q: %w", blueprintID, err)
	}
	template := V2DefinitionTemplate(blueprintID)
	if template == nil {
		return nil, fmt.Errorf("work: BeginBlueprintPlanning: Blueprint %q has no V2 definition", blueprintID)
	}

	selectedRef := BlueprintRef{ID: bp.ID, SchemaVersion: bp.SchemaVersion, Version: bp.Version}
	view, err := s.beginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: strings.TrimSpace(input.SessionID),
		RequestID: blueprintChildRequestID(requestID, "begin"),
	}, selectedRef)
	if err != nil {
		return nil, fmt.Errorf("work: BeginBlueprintPlanning: begin: %w", err)
	}
	template.WorkID = view.Work.ID

	_, state, err := s.store.LoadState(view.Work.ID, "")
	if err != nil {
		return nil, fmt.Errorf("work: BeginBlueprintPlanning: load candidate base: %w", err)
	}
	candidate, err := s.CreateCandidateRevision(
		ctx,
		view.Work.ID,
		template,
		blueprintChildRequestID(requestID, "candidate"),
		state.Revision,
	)
	if err != nil {
		return nil, fmt.Errorf("work: BeginBlueprintPlanning: candidate: %w", err)
	}

	_, state, err = s.store.LoadState(view.Work.ID, "")
	if err != nil {
		return nil, fmt.Errorf("work: BeginBlueprintPlanning: load apply base: %w", err)
	}
	applied, err := s.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID:           view.Work.ID,
		Revision:         candidate.Revision,
		ExpectedRevision: state.Revision,
		RequestID:        blueprintChildRequestID(requestID, "apply"),
	})
	if err != nil {
		return nil, fmt.Errorf("work: BeginBlueprintPlanning: apply: %w", err)
	}

	return &BeginBlueprintPlanningResult{
		BlueprintRef:       selectedRef,
		WorkID:             view.Work.ID,
		DefinitionRevision: candidate.Revision,
		Apply:              applied,
	}, nil
}

func blueprintChildRequestID(requestID, phase string) string {
	sum := sha256.Sum256([]byte(requestID + "\x00" + phase))
	return fmt.Sprintf("blueprint-%x", sum[:])
}

// ── Scenario 1: Image Compile ───────────────────────────────────────────────

func builtinImageCompile() *WorkBlueprint {
	return &WorkBlueprint{
		SchemaVersion:  SchemaVersion,
		ID:             "blueprint:image-compile",
		Version:        1,
		Name:           "图片编译",
		Description:    "批量处理图片素材：检查、裁切、统一色彩、导出压缩，生成最终图片和社交素材包。",
		Source:         BlueprintSystem,
		InputSchema:    nil,
		PromptTemplate: "请处理以下图片素材，按指定尺寸、格式和裁切策略进行批量处理。",
		Workflow: WorkflowDef{
			Stages: []StageSpec{
				{ID: "pic-check", Title: "素材检查", Tasks: []TaskSpec{{ID: "pic-check-files", Title: "检查素材完整性"}}},
				{ID: "pic-process", Title: "批量处理", Tasks: []TaskSpec{
					{ID: "pic-crop", Title: "批量裁切"},
					{ID: "pic-color", Title: "统一色彩"},
				}},
				{ID: "pic-export", Title: "导出", Tasks: []TaskSpec{{ID: "pic-compress", Title: "导出压缩"}}},
			},
		},
		BlockSpecs: []BlockSpec{
			{ID: "pic-materials", Kind: "file_list", SchemaVersion: 1, Label: "素材文件", Description: "待处理的图片素材", Placement: BlockPlacement{Slot: "primary", Order: 0}, Editable: true},
			{ID: "pic-result", Kind: "artifact", SchemaVersion: 1, Label: "处理结果", Description: "最终图片和素材包", Placement: BlockPlacement{Slot: "result", Order: 1}, Editable: false},
		},
		ArtifactKinds: []string{"image", "zip"},
		ToolContracts: []ToolContractRef{},
		CreatedAt:     time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
	}
}

func v2DefImageCompile() *WorkDefinitionRevision {
	return &WorkDefinitionRevision{
		Goal: "批量处理图片素材：检查、裁切、统一色彩、导出压缩",
		Nodes: []NodeDef{
			{
				ID:              "material-check",
				Title:           "素材检查",
				Description:     "检查素材文件完整性、格式和分辨率",
				InputSpecIDs:    []string{"source-materials", "target-size", "output-format", "crop-strategy"},
				ProducesSlotIDs: []string{"checked-materials"},
			},
			{
				ID:              "batch-crop",
				Title:           "批量裁切",
				Description:     "按目标尺寸批量裁切所有素材",
				DependsOn:       []string{"material-check"},
				ProducesSlotIDs: []string{"cropped-previews"},
			},
			{
				ID:          "color-unify",
				Title:       "统一色彩",
				Description: "统一色彩配置文件和色调",
				DependsOn:   []string{"batch-crop"},
			},
			{
				ID:              "export-compress",
				Title:           "导出压缩",
				Description:     "按目标格式导出并压缩，生成最终图片、社交素材包和缩略图",
				DependsOn:       []string{"color-unify"},
				ProducesSlotIDs: []string{"final-image", "social-pack", "thumbnails"},
			},
		},
		ArtifactSlots: []ArtifactSlotDef{
			{ID: "checked-materials", Title: "素材检查报告", Kind: "text", ExpectedCount: 1, Required: false},
			{ID: "cropped-previews", Title: "裁切预览", Kind: "image", ExpectedCount: 1, Required: false},
			{ID: "final-image", Title: "最终图片", Kind: "image", ExpectedCount: 1, Required: true},
			{ID: "social-pack", Title: "社交素材包", Kind: "zip", ExpectedCount: 1, Required: true},
			{ID: "thumbnails", Title: "缩略图", Kind: "image", ExpectedCount: 1, Required: false},
		},
		InputSpecs: []InputSpec{
			{
				ID: "source-materials", Label: "素材文件", Kind: InputFile, Required: true,
				Description: "上传待处理的图片素材",
			},
			{
				ID: "target-size", Label: "目标尺寸", Kind: InputChoice, Required: true,
				Description: "选择输出尺寸",
				ValueSchema: json.RawMessage(`{"options":[{"value":"1920x1080"},{"value":"1080x1080"},{"value":"800x600"},{"value":"custom"}]}`),
			},
			{
				ID: "output-format", Label: "输出格式", Kind: InputChoice, Required: true,
				Description: "选择输出图片格式",
				ValueSchema: json.RawMessage(`{"options":[{"value":"PNG"},{"value":"JPEG"},{"value":"WebP"},{"value":"AVIF"}]}`),
			},
			{
				ID: "crop-strategy", Label: "裁切策略", Kind: InputChoice, Required: false,
				Description:  "选择裁切方式",
				ValueSchema:  json.RawMessage(`{"options":[{"value":"center"},{"value":"top"},{"value":"smart"},{"value":"none"}]}`),
				DefaultValue: json.RawMessage(`"center"`),
			},
		},
		CreatedBy: "system",
	}
}

// ── Scenario 2: Script Writing ──────────────────────────────────────────────

func builtinScriptWriting() *WorkBlueprint {
	return &WorkBlueprint{
		SchemaVersion:  SchemaVersion,
		ID:             "blueprint:script-writing",
		Version:        1,
		Name:           "剧本生成",
		Description:    "梳理主题、构建人物、并行写分场、连续性检查，生成完整剧本、分场表和角色小传。",
		Source:         BlueprintSystem,
		InputSchema:    nil,
		PromptTemplate: "请根据给定主题和人物设定，生成剧本、分场表和角色小传。",
		Workflow: WorkflowDef{
			Stages: []StageSpec{
				{ID: "script-prep", Title: "创作准备", Tasks: []TaskSpec{
					{ID: "script-theme", Title: "梳理主题"},
					{ID: "script-char", Title: "构建人物"},
				}},
				{ID: "script-write", Title: "分场创作", Tasks: []TaskSpec{
					{ID: "script-scene-a", Title: "分场 A"},
					{ID: "script-scene-b", Title: "分场 B"},
					{ID: "script-scene-c", Title: "分场 C"},
				}},
				{ID: "script-check", Title: "连续性检查", Tasks: []TaskSpec{{ID: "script-continuity", Title: "整体连续性检查"}}},
			},
		},
		BlockSpecs: []BlockSpec{
			{ID: "script-params", Kind: "input", SchemaVersion: 1, Label: "创作参数", Description: "受众、时长、基调、内容禁区", Placement: BlockPlacement{Slot: "primary", Order: 0}, Editable: true},
			{ID: "script-result", Kind: "artifact", SchemaVersion: 1, Label: "剧本成果", Description: "剧本、分场表和角色小传", Placement: BlockPlacement{Slot: "result", Order: 1}, Editable: false},
		},
		ArtifactKinds: []string{"markdown", "docx"},
		ToolContracts: []ToolContractRef{},
		CreatedAt:     time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
	}
}

func v2DefScriptWriting() *WorkDefinitionRevision {
	return &WorkDefinitionRevision{
		Goal: "根据主题和人物设定生成完整剧本、分场表和角色小传",
		Nodes: []NodeDef{
			{
				ID:              "theme-outline",
				Title:           "梳理主题",
				Description:     "确定剧本主题、基调和核心冲突",
				InputSpecIDs:    []string{"audience", "duration", "tone", "restrictions"},
				ProducesSlotIDs: []string{"theme-outline"},
			},
			{
				ID:              "character-build",
				Title:           "构建人物",
				Description:     "创建主要角色小传和关系图谱",
				DependsOn:       []string{"theme-outline"},
				ProducesSlotIDs: []string{"character-bios"},
			},
			{
				ID:          "scene-a",
				Title:       "分场 A",
				Description: "创作第一幕分场内容",
				DependsOn:   []string{"character-build"},
			},
			{
				ID:          "scene-b",
				Title:       "分场 B",
				Description: "创作第二幕分场内容",
				DependsOn:   []string{"character-build"},
			},
			{
				ID:          "scene-c",
				Title:       "分场 C",
				Description: "创作第三幕分场内容",
				DependsOn:   []string{"character-build"},
			},
			{
				ID:              "continuity-check",
				Title:           "连续性检查",
				Description:     "整体检查人物行为、时间线和剧情逻辑一致性",
				DependsOn:       []string{"scene-a", "scene-b", "scene-c"},
				ProducesSlotIDs: []string{"final-script", "scene-table"},
			},
		},
		ArtifactSlots: []ArtifactSlotDef{
			{ID: "theme-outline", Title: "主题大纲", Kind: "text", ExpectedCount: 1, Required: false},
			{ID: "character-bios", Title: "角色小传", Kind: "docx", ExpectedCount: 1, Required: true},
			{ID: "final-script", Title: "最终剧本", Kind: "docx", ExpectedCount: 1, Required: true},
			{ID: "scene-table", Title: "分场表", Kind: "docx", ExpectedCount: 1, Required: true},
		},
		InputSpecs: []InputSpec{
			{
				ID: "audience", Label: "目标受众", Kind: InputText, Required: true,
				Description: "剧本面向的观众群体",
			},
			{
				ID: "duration", Label: "时长要求", Kind: InputText, Required: false,
				Description: "预计剧目时长",
			},
			{
				ID: "tone", Label: "基调", Kind: InputChoice, Required: true,
				Description: "剧本的整体基调",
				ValueSchema: json.RawMessage(`{"options":[{"value":"正剧"},{"value":"喜剧"},{"value":"悲剧"},{"value":"悬疑"},{"value":"科幻"},{"value":"爱情"}]}`),
			},
			{
				ID: "restrictions", Label: "内容禁区", Kind: InputText, Required: false,
				Description: "需要避免的主题和内容限制",
			},
		},
		CreatedBy: "system",
	}
}

// ── Scenario 3: Financial Budget ────────────────────────────────────────────

func builtinFinancialBudget() *WorkBlueprint {
	return &WorkBlueprint{
		SchemaVersion:  SchemaVersion,
		ID:             "blueprint:financial-budget",
		Version:        1,
		Name:           "财务预算",
		Description:    "汇总历史数据、测算部门预算、现金流压力测试、生成预算说明文档。产出预算表、说明和情景分析。",
		Source:         BlueprintSystem,
		InputSchema:    nil,
		PromptTemplate: "请根据历史数据和预算参数，生成部门预算表、预算说明和情景分析。",
		Workflow: WorkflowDef{
			Stages: []StageSpec{
				{ID: "budget-prep", Title: "数据准备", Tasks: []TaskSpec{{ID: "budget-history", Title: "汇总历史数据"}}},
				{ID: "budget-calc", Title: "预算测算", Tasks: []TaskSpec{
					{ID: "budget-dept", Title: "测算部门预算"},
					{ID: "budget-cashflow", Title: "现金流压力测试"},
				}},
				{ID: "budget-output", Title: "输出", Tasks: []TaskSpec{{ID: "budget-narrative", Title: "生成预算说明"}}},
			},
		},
		BlockSpecs: []BlockSpec{
			{ID: "budget-params", Kind: "input", SchemaVersion: 1, Label: "预算参数", Description: "上限、币种、部门调整、审批阈值", Placement: BlockPlacement{Slot: "primary", Order: 0}, Editable: true},
			{ID: "budget-result", Kind: "artifact", SchemaVersion: 1, Label: "预算成果", Description: "预算表、说明和情景分析", Placement: BlockPlacement{Slot: "result", Order: 1}, Editable: false},
		},
		ArtifactKinds: []string{"xlsx", "docx"},
		ToolContracts: []ToolContractRef{},
		CreatedAt:     time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
	}
}

func v2DefFinancialBudget() *WorkDefinitionRevision {
	return &WorkDefinitionRevision{
		Goal: "根据历史数据和参数生成部门预算表、预算说明和情景分析",
		Nodes: []NodeDef{
			{
				ID:              "history-summary",
				Title:           "汇总历史数据",
				Description:     "汇总和分析历史财务数据作为预算基准",
				InputSpecIDs:    []string{"budget-ceiling", "currency", "dept-adjustments"},
				ProducesSlotIDs: []string{"history-baseline"},
			},
			{
				ID:              "dept-budget",
				Title:           "测算部门预算",
				Description:     "按部门测算预算分配",
				DependsOn:       []string{"history-summary"},
				ProducesSlotIDs: []string{"budget-spreadsheet"},
			},
			{
				ID:              "cashflow-test",
				Title:           "现金流压力测试",
				Description:     "模拟不同情景下的现金流状况",
				DependsOn:       []string{"dept-budget"},
				ProducesSlotIDs: []string{"scenario-analysis"},
			},
			{
				ID:              "narrative-generate",
				Title:           "生成预算说明",
				Description:     "生成预算说明文档，包含假设、风险和建议",
				DependsOn:       []string{"cashflow-test"},
				InputSpecIDs:    []string{"approval-threshold"},
				ProducesSlotIDs: []string{"budget-narrative"},
			},
		},
		ArtifactSlots: []ArtifactSlotDef{
			{ID: "history-baseline", Title: "历史基准", Kind: "text", ExpectedCount: 1, Required: false},
			{ID: "budget-spreadsheet", Title: "预算表", Kind: "xlsx", ExpectedCount: 1, Required: true},
			{ID: "scenario-analysis", Title: "情景分析", Kind: "xlsx", ExpectedCount: 1, Required: true},
			{ID: "budget-narrative", Title: "预算说明", Kind: "docx", ExpectedCount: 1, Required: true},
		},
		InputSpecs: []InputSpec{
			{
				ID: "budget-ceiling", Label: "预算上限", Kind: InputNumber, Required: true,
				Description: "总预算上限金额",
			},
			{
				ID: "currency", Label: "币种", Kind: InputChoice, Required: true,
				Description:  "预算币种",
				ValueSchema:  json.RawMessage(`{"options":[{"value":"CNY"},{"value":"USD"},{"value":"EUR"},{"value":"JPY"},{"value":"GBP"}]}`),
				DefaultValue: json.RawMessage(`"CNY"`),
			},
			{
				ID: "dept-adjustments", Label: "部门调整", Kind: InputForm, Required: false,
				Description: "各部门预算调整系数",
				ValueSchema: json.RawMessage(`{"type":"object"}`),
			},
			{
				ID: "approval-threshold", Label: "审批阈值", Kind: InputNumber, Required: false,
				Description: "单笔超出此金额需额外审批",
			},
		},
		CreatedBy: "system",
	}
}

// ── Scenario 4: Git Release ─────────────────────────────────────────────────

func builtinGitRelease() *WorkBlueprint {
	return &WorkBlueprint{
		SchemaVersion:  SchemaVersion,
		ID:             "blueprint:git-release",
		Version:        1,
		Name:           "Git 发版",
		Description:    "只读检查、测试、变更日志、构建签名、人工批准后发布。产出 Release Notes、构建包。",
		Source:         BlueprintSystem,
		InputSchema:    nil,
		PromptTemplate: "请对目标分支执行发版流程：检查、测试、生成变更日志、构建、签名，等待批准后发布。",
		Workflow: WorkflowDef{
			Stages: []StageSpec{
				{ID: "release-check", Title: "发布检查", Tasks: []TaskSpec{
					{ID: "release-readonly", Title: "只读检查"},
					{ID: "release-test", Title: "运行测试"},
				}},
				{ID: "release-prep", Title: "发布准备", Tasks: []TaskSpec{
					{ID: "release-changelog", Title: "生成变更日志"},
					{ID: "release-build", Title: "构建和签名"},
				}},
				{ID: "release-publish", Title: "发布", Tasks: []TaskSpec{{ID: "release-push", Title: "推送发布"}}, Gate: "approval"},
			},
		},
		BlockSpecs: []BlockSpec{
			{ID: "release-params", Kind: "input", SchemaVersion: 1, Label: "发布参数", Description: "版本号、分支、渠道、窗口", Placement: BlockPlacement{Slot: "primary", Order: 0}, Editable: true},
			{ID: "release-approval", Kind: "approval", SchemaVersion: 1, Label: "发布批准", Description: "确认发布前需人工批准", Placement: BlockPlacement{Slot: "primary", Order: 1}, Editable: false},
			{ID: "release-result", Kind: "artifact", SchemaVersion: 1, Label: "发布成果", Description: "Release Notes 和构建包", Placement: BlockPlacement{Slot: "result", Order: 2}, Editable: false},
		},
		ArtifactKinds: []string{"markdown", "zip"},
		ToolContracts: []ToolContractRef{
			{Name: "git", ContractVersion: 1, SideEffectClass: "read", Required: false},
		},
		CreatedAt: time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
	}
}

func v2DefGitRelease() *WorkDefinitionRevision {
	return &WorkDefinitionRevision{
		Goal: "对目标分支执行只读检查、测试、变更日志、构建签名，人工批准后发布",
		Nodes: []NodeDef{
			{
				ID:              "readonly-check",
				Title:           "只读检查",
				Description:     "检查分支状态、代码规范、依赖完整性和安全扫描（仅只读操作）",
				InputSpecIDs:    []string{"version-number", "target-branch"},
				ProducesSlotIDs: []string{"check-report"},
				ToolHints:       []string{"git:side_effect=read"},
			},
			{
				ID:              "test-run",
				Title:           "运行测试",
				Description:     "执行单元测试和集成测试",
				DependsOn:       []string{"readonly-check"},
				ProducesSlotIDs: []string{"test-report"},
				ToolHints:       []string{"side_effect=read"},
			},
			{
				ID:              "changelog",
				Title:           "生成变更日志",
				Description:     "从提交历史生成 Release Notes",
				DependsOn:       []string{"test-run"},
				ProducesSlotIDs: []string{"release-notes"},
				ToolHints:       []string{"side_effect=read"},
			},
			{
				ID:              "build-sign",
				Title:           "构建和签名",
				Description:     "编译构建包并签名",
				DependsOn:       []string{"changelog"},
				ProducesSlotIDs: []string{"build-package"},
				ToolHints:       []string{"side_effect=workspace_write"},
				InputSpecIDs:    []string{"release-channel", "release-window"},
			},
			{
				ID:              "release-publish",
				Title:           "推送发布",
				Description:     "推送构建包到发布渠道。此操作不可逆，需人工批准。",
				DependsOn:       []string{"build-sign"},
				InputSpecIDs:    []string{"release-approval"},
				ProducesSlotIDs: []string{"release-version"},
				ToolHints:       []string{"side_effect=external_write"},
				GlobalGate:      "release-gate",
			},
		},
		ArtifactSlots: []ArtifactSlotDef{
			{ID: "check-report", Title: "检查报告", Kind: "text", ExpectedCount: 1, Required: false},
			{ID: "test-report", Title: "测试报告", Kind: "text", ExpectedCount: 1, Required: false},
			{ID: "release-notes", Title: "Release Notes", Kind: "markdown", ExpectedCount: 1, Required: true},
			{ID: "build-package", Title: "构建包", Kind: "zip", ExpectedCount: 1, Required: true},
			{ID: "release-version", Title: "发布版本", Kind: "text", ExpectedCount: 1, Required: true},
		},
		InputSpecs: []InputSpec{
			{
				ID: "version-number", Label: "版本号", Kind: InputText, Required: true,
				Description: "语义化版本号，例如 2.1.0",
			},
			{
				ID: "target-branch", Label: "目标分支", Kind: InputText, Required: true,
				Description:  "要发布的分支名称",
				DefaultValue: json.RawMessage(`"main"`),
			},
			{
				ID: "release-channel", Label: "发布渠道", Kind: InputChoice, Required: true,
				Description:  "选择发布渠道",
				ValueSchema:  json.RawMessage(`{"options":[{"value":"stable"},{"value":"beta"},{"value":"canary"}]}`),
				DefaultValue: json.RawMessage(`"stable"`),
			},
			{
				ID: "release-window", Label: "发布窗口", Kind: InputDate, Required: false,
				Description: "计划发布时间窗口",
			},
			{
				ID: "release-approval", Label: "发布批准", Kind: InputApproval, Required: true,
				Description: "发布为不可逆操作，必须显式批准。批准前零发布副作用。",
			},
		},
		CreatedBy: "system",
	}
}

// ── Scenario 5: Annual Event ────────────────────────────────────────────────

func builtinAnnualEvent() *WorkBlueprint {
	return &WorkBlueprint{
		SchemaVersion:  SchemaVersion,
		ID:             "blueprint:annual-event",
		Version:        1,
		Name:           "年会筹划",
		Description:    "筛选场地、编排议程、汇总人员、设计物料、协调供应商，产出年会方案、预算和节目单。",
		Source:         BlueprintSystem,
		InputSchema:    nil,
		PromptTemplate: "请根据城市、日期、预算和人员名单，筹划年会方案、预算和节目单。",
		Workflow: WorkflowDef{
			Stages: []StageSpec{
				{ID: "event-plan", Title: "方案规划", Tasks: []TaskSpec{
					{ID: "event-venue", Title: "筛选场地"},
					{ID: "event-agenda", Title: "编排议程"},
				}},
				{ID: "event-people", Title: "人员物料", Tasks: []TaskSpec{
					{ID: "event-roster", Title: "汇总人员名单"},
					{ID: "event-material", Title: "设计物料"},
				}},
				{ID: "event-vendor", Title: "供应商协调", Tasks: []TaskSpec{{ID: "event-coordinate", Title: "协调供应商"}}},
			},
		},
		BlockSpecs: []BlockSpec{
			{ID: "event-roster-blk", Kind: "list", SchemaVersion: 1, Label: "参会名单", Description: "需参会的人员名单（必须填入）", Placement: BlockPlacement{Slot: "primary", Order: 0}, Editable: true},
			{ID: "event-params", Kind: "input", SchemaVersion: 1, Label: "筹划参数", Description: "城市、日期、预算、餐饮禁忌", Placement: BlockPlacement{Slot: "primary", Order: 1}, Editable: true},
			{ID: "event-result", Kind: "artifact", SchemaVersion: 1, Label: "年会成果", Description: "方案、预算和节目单", Placement: BlockPlacement{Slot: "result", Order: 2}, Editable: false},
		},
		ArtifactKinds: []string{"docx", "xlsx"},
		ToolContracts: []ToolContractRef{},
		CreatedAt:     time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
	}
}

func v2DefAnnualEvent() *WorkDefinitionRevision {
	return &WorkDefinitionRevision{
		Goal: "根据城市、日期、预算和人员名单筹划年会方案、预算和节目单",
		Nodes: []NodeDef{
			{
				ID:              "venue-filter",
				Title:           "筛选场地",
				Description:     "根据城市、预算和人数筛选推荐场地",
				InputSpecIDs:    []string{"event-city", "event-date", "event-budget"},
				ProducesSlotIDs: []string{"venue-options"},
			},
			{
				ID:              "agenda-arrange",
				Title:           "编排议程",
				Description:     "编排年会日程和节目流程",
				DependsOn:       []string{"venue-filter"},
				ProducesSlotIDs: []string{"event-plan", "program-list"},
			},
			{
				ID:              "roster-collect",
				Title:           "汇总人员名单",
				Description:     "汇总参会人员名单，包含姓名、部门、饮食禁忌。名单需通过 Block typed input 补充。",
				InputSpecIDs:    []string{"event-roster", "dietary-restrictions"},
				ProducesSlotIDs: []string{"roster-summary"},
			},
			{
				ID:          "material-design",
				Title:       "设计物料",
				Description: "设计年会邀请函、背景板、桌牌等物料",
				DependsOn:   []string{"roster-collect"},
			},
			{
				ID:              "vendor-coordinate",
				Title:           "协调供应商",
				Description:     "协调餐饮、摄影、音响等供应商",
				DependsOn:       []string{"agenda-arrange", "material-design"},
				ProducesSlotIDs: []string{"event-budget"},
			},
		},
		ArtifactSlots: []ArtifactSlotDef{
			{ID: "venue-options", Title: "场地备选", Kind: "text", ExpectedCount: 1, Required: false},
			{ID: "event-plan", Title: "年会方案", Kind: "docx", ExpectedCount: 1, Required: true},
			{ID: "program-list", Title: "节目单", Kind: "docx", ExpectedCount: 1, Required: true},
			{ID: "roster-summary", Title: "人员汇总", Kind: "text", ExpectedCount: 1, Required: true},
			{ID: "event-budget", Title: "年会预算", Kind: "xlsx", ExpectedCount: 1, Required: true},
		},
		InputSpecs: []InputSpec{
			{
				ID: "event-city", Label: "举办城市", Kind: InputChoice, Required: true,
				Description: "选择年会举办城市",
				ValueSchema: json.RawMessage(`{"options":[{"value":"北京"},{"value":"上海"},{"value":"广州"},{"value":"深圳"},{"value":"杭州"},{"value":"成都"},{"value":"三亚"}]}`),
			},
			{
				ID: "event-date", Label: "举办日期", Kind: InputDate, Required: true,
				Description: "年会日期",
			},
			{
				ID: "event-budget", Label: "预算总额", Kind: InputNumber, Required: true,
				Description: "年会总预算",
			},
			{
				ID: "event-roster", Label: "参会名单", Kind: InputRoster, Required: true,
				Description: "参会人员名单（姓名、部门），必须通过 Block typed input 补充。不可为空。",
				ValueSchema: json.RawMessage(`{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"department":{"type":"string"}},"required":["name"]}}`),
			},
			{
				ID: "dietary-restrictions", Label: "餐饮禁忌", Kind: InputText, Required: false,
				Description: "参会人员的餐饮禁忌和特殊需求",
			},
		},
		CreatedBy: "system",
	}
}
