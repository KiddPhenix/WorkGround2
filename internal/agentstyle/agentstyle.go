// Package agentstyle holds the built-in catalog of selectable Agent personality
// styles. Each style is a stable ASCII ID paired with the exact Chinese product
// label (病名｜风格名) and a capability description; boot folds the selected
// styles into the cache-stable system prompt. The catalog is the single source
// of truth for both the desktop Settings picker and the prompt compiler.
package agentstyle

import (
	"fmt"
	"strings"
)

// Style is one selectable Agent personality style. Disorder is the original
// personality/disorder name (e.g. "偏执型"), StyleName the human-facing style
// name (e.g. "风险审查者"), and Capability the exact capability text that must
// appear verbatim in the compiled prompt block.
type Style struct {
	ID         string
	Disorder   string
	StyleName  string
	Capability string
}

// catalog is the ordered product data. The slice order is the deterministic
// catalog order and must not be reordered casually: config persistence and the
// desktop Settings picker both depend on it.
var catalog = []Style{
	{
		ID:         "paranoid",
		Disorder:   "偏执型",
		StyleName:  "风险审查者",
		Capability: "保持高度警觉，寻找隐藏假设、利益冲突、欺诈风险和安全漏洞",
	},
	{
		ID:         "schizoid",
		Disorder:   "分裂样型",
		StyleName:  "独立深思者",
		Capability: "独立分析，不迎合群体意见，减少社交噪声，专注长期逻辑与问题本质",
	},
	{
		ID:         "schizotypal",
		Disorder:   "分裂型",
		StyleName:  "非传统创意者",
		Capability: "建立跨领域联想，提出反常规假设和原创方案",
	},
	{
		ID:         "antisocial",
		Disorder:   "反社会型",
		StyleName:  "冷静决策者",
		Capability: "在压力下保持冷静、果断和风险承受力，敢于挑战无效规则",
	},
	{
		ID:         "borderline",
		Disorder:   "边缘型",
		StyleName:  "情绪洞察者",
		Capability: "敏锐识别情绪、关系变化和被忽视的需求，保持投入与创造力",
	},
	{
		ID:         "histrionic",
		Disorder:   "表演型",
		StyleName:  "表达传播者",
		Capability: "将内容表达得鲜明、有感染力、易记，善用故事和受众视角",
	},
	{
		ID:         "narcissistic",
		Disorder:   "自恋型",
		StyleName:  "自信进取者",
		Capability: "提出有野心的目标，展示领导力、自信和高标准",
	},
	{
		ID:         "avoidant",
		Disorder:   "回避型",
		StyleName:  "审慎预演者",
		Capability: "提前发现失败、拒绝和负面反馈风险，充分准备",
	},
	{
		ID:         "dependent",
		Disorder:   "依赖型",
		StyleName:  "协作支持者",
		Capability: "重视合作、忠诚、共识、求助和团队稳定",
	},
	{
		ID:         "obsessive_compulsive",
		Disorder:   "强迫型人格",
		StyleName:  "严谨执行者",
		Capability: "强调秩序、细节、质量、检查清单和可重复流程",
	},
}

// Catalog returns an immutable copy of the built-in catalog in deterministic
// catalog order. Callers may reorder or mutate the returned slice without
// affecting the package's source of truth.
func Catalog() []Style {
	out := make([]Style, len(catalog))
	copy(out, catalog)
	return out
}

// ByID resolves a style by its exact, case-insensitive ASCII ID. The boolean is
// false for an empty or unknown ID so callers can fail explicitly instead of
// guessing.
func ByID(id string) (Style, bool) {
	id = NormalizeID(id)
	for _, st := range catalog {
		if st.ID == id {
			return st, true
		}
	}
	return Style{}, false
}

// NormalizeID trims and lowercases an ID for comparisons.
func NormalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// Canonicalize validates and canonicalizes a list of IDs: empty/blank entries are
// dropped, duplicates collapse to one, and the result is returned in catalog
// order. Unknown IDs are an error — they are surfaced (never silently dropped) so
// the write boundary can reject them instead of persisting a typo.
func Canonicalize(ids []string) ([]string, error) {
	canonical, unknown := ResolveIDs(ids)
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown agent prompt style(s): %s", strings.Join(unknown, ", "))
	}
	return canonical, nil
}

// ResolveIDs separates known and unknown IDs without sacrificing valid
// selections. Known IDs are deduplicated in catalog order; unknown IDs are
// deduplicated in input order so boot can warn and keep the recoverable subset.
func ResolveIDs(ids []string) (canonical, unknown []string) {
	selected := make(map[string]bool, len(ids))
	unknownSet := make(map[string]bool)
	for _, raw := range ids {
		id := NormalizeID(raw)
		if id == "" {
			continue
		}
		st, ok := ByID(id)
		if !ok {
			if !unknownSet[id] {
				unknown = append(unknown, id)
				unknownSet[id] = true
			}
			continue
		}
		selected[st.ID] = true
	}
	canonical = make([]string, 0, len(selected))
	for _, st := range catalog {
		if selected[st.ID] {
			canonical = append(canonical, st.ID)
		}
	}
	return canonical, unknown
}

// Compile renders the deterministic system-prompt block for the selected IDs.
// An empty selection returns an empty block and no error. The prompt contains a
// single 风格 prefix followed only by the selected capability texts; catalog
// labels remain Settings-only metadata. The same canonical ID set always
// produces the same bytes.
func Compile(ids []string) (string, error) {
	canonical, err := Canonicalize(ids)
	if err != nil {
		return "", err
	}
	if len(canonical) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("风格: ")
	for i, id := range canonical {
		if i > 0 {
			b.WriteByte('\n')
		}
		st, _ := ByID(id)
		b.WriteString(st.Capability)
	}
	return b.String(), nil
}
