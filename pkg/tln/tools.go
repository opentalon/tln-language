package tln

import (
	"reflect"
	"sort"

	"github.com/opentalon/tln-language/internal/ast"
)

// ToolRef is one MCP tool a program references: the Server (the provider /
// connector name, e.g. "timly-api") and the Tool (the operation name, e.g.
// "list_items"). It mirrors a single `tool "<server>" "<tool>"` call.
type ToolRef struct {
	Server string `json:"server"`
	Tool   string `json:"tool"`
}

// ToolReferences compiles the source (same pipeline as [Check]) and returns the
// distinct set of MCP tools the program calls — every `tool "<server>"
// "<tool>"` reference across all blocks (workflow steps, detect/rule/recommend
// remediations, …), deduplicated and sorted by (server, tool).
//
// Callers use it to persist a per-agent "tool manifest" at create/update time
// so a later API change can enumerate which stored workflows reference a given
// provider/operation and need migrating. Because the program is the fully
// resolved, macro-expanded AST, references introduced by imports or macros are
// included. Test-block mocks and `tool_called` assertions are not real calls
// and are excluded (the whole `test { … }` subtree is skipped). Invalid source
// returns the compile error unchanged, exactly like [Check].
func ToolReferences(src string, opts ...Option) ([]ToolRef, error) {
	cfg := &runConfig{file: "<tln>"}
	for _, opt := range opts {
		opt(cfg)
	}
	prog, _, err := compileProgram(cfg.file, src)
	if err != nil {
		return nil, err
	}
	seen := map[ToolRef]bool{}
	refs := []ToolRef{}
	collectToolRefs(reflect.ValueOf(prog), seen, &refs, map[uintptr]bool{})
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Server != refs[j].Server {
			return refs[i].Server < refs[j].Server
		}
		return refs[i].Tool < refs[j].Tool
	})
	return refs, nil
}

var (
	mcpCallType   = reflect.TypeOf(ast.MCPCall{})
	testBlockType = reflect.TypeOf(ast.TestBlock{})
)

// collectToolRefs walks the AST reflectively and collects every *ast.MCPCall.
// Reflection (rather than an explicit per-block visitor) keeps the walk correct
// as new block types are added — any block that embeds an MCPCall is covered
// automatically. Field values reached through unexported fields are read-only;
// Kind/Elem/Field/Index/MapIndex and the String getter all work on them, so no
// field needs to be exported for this to traverse. The visited set guards
// against cycles/shared pointers.
func collectToolRefs(v reflect.Value, seen map[ToolRef]bool, out *[]ToolRef, visited map[uintptr]bool) {
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return
		}
		p := v.Pointer()
		if visited[p] {
			return
		}
		visited[p] = true
		collectToolRefs(v.Elem(), seen, out, visited)
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		collectToolRefs(v.Elem(), seen, out, visited)
	case reflect.Struct:
		switch v.Type() {
		case testBlockType:
			// A test block's mocks/assertions are not real tool calls.
			return
		case mcpCallType:
			ref := ToolRef{
				Server: v.FieldByName("Server").String(),
				Tool:   v.FieldByName("Tool").String(),
			}
			if (ref.Server != "" || ref.Tool != "") && !seen[ref] {
				seen[ref] = true
				*out = append(*out, ref)
			}
			return
		}
		for i := 0; i < v.NumField(); i++ {
			collectToolRefs(v.Field(i), seen, out, visited)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			collectToolRefs(v.Index(i), seen, out, visited)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			collectToolRefs(v.MapIndex(k), seen, out, visited)
		}
	}
}
