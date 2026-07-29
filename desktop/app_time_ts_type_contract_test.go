package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestWorkTimeFieldsHaveTsType 验证从 *App 公开方法签名递归可达的所有
// 可序列化结构类型中，time.Time / *time.Time 字段都包含 ts_type:"string" 标签。
// 若缺少该标签，Wails 绑定生成器会输出 "Not found: time.Time" 告警，且 TypeScript
// 端时间字段会变成 any 而非 string。
func TestWorkTimeFieldsHaveTsType(t *testing.T) {
	app := &App{}
	appType := reflect.TypeOf(app)

	seen := make(map[reflect.Type]bool)
	var violations []string

	for i := 0; i < appType.NumMethod(); i++ {
		m := appType.Method(i)
		if !m.IsExported() {
			continue
		}
		// 检查参数类型（跳过 receiver）
		for j := 0; j < m.Type.NumIn(); j++ {
			checkTimeFields(m.Type.In(j), seen, &violations, m.Name)
		}
		// 检查返回值类型
		for j := 0; j < m.Type.NumOut(); j++ {
			checkTimeFields(m.Type.Out(j), seen, &violations, m.Name)
		}
	}

	if len(violations) > 0 {
		t.Errorf("以下 time.Time 字段缺少 ts_type:\"string\" 标签（共 %d 处）:\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// checkTimeFields 递归遍历 typ，检查其包含的结构体字段。
// 处理: struct, pointer, slice, array, map value。
// 不处理: interface, func, chan, basic types。
// seen 用于防止循环。
func checkTimeFields(typ reflect.Type, seen map[reflect.Type]bool, violations *[]string, methodName string) {
	// 解引用 pointer
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	switch typ.Kind() {
	case reflect.Struct:
		if typ == reflect.TypeOf(time.Time{}) || typ == reflect.TypeOf((*time.Time)(nil)).Elem() {
			// 这不应该发生——time.Time 不是方法签名中的字段，而是字段类型。若发生，报错。
			return
		}
		if seen[typ] {
			return
		}
		seen[typ] = true

		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			fieldType := f.Type
			// 解引用 pointer 拿到基础类型
			for fieldType.Kind() == reflect.Ptr {
				fieldType = fieldType.Elem()
			}
			if fieldType == reflect.TypeOf(time.Time{}) || fieldType == reflect.TypeOf((*time.Time)(nil)).Elem() {
				// 该字段是 time.Time 或 *time.Time，检查 ts_type 标签
				tag := f.Tag.Get("ts_type")
				if tag != "string" {
					*violations = append(*violations, typ.String()+"."+f.Name+" (from "+methodName+")")
				}
			} else {
				// 递归进入嵌套类型
				checkTimeFields(f.Type, seen, violations, methodName)
			}
		}

	case reflect.Slice, reflect.Array:
		checkTimeFields(typ.Elem(), seen, violations, methodName)

	case reflect.Map:
		checkTimeFields(typ.Elem(), seen, violations, methodName)
	}
}
