package server

import (
	_ "embed"
	"strings"
)

//go:embed zcode_system.json
var zcodeSystemJSON []byte

// Keep the upstream client template separate from caller instructions. Omitting
// it causes otherwise valid inference requests to be rejected with code 3012.
var zcodeUpstreamSystemPrefix = func() []any {
	blocks := arr(parse(string(zcodeSystemJSON), nil))
	if len(blocks) != 3 {
		panic("zcode_system.json must contain exactly three system blocks")
	}
	return blocks
}()

func upstreamSystemBlocks(client []any) ([]any, error) {
	system := append(append([]any{}, zcodeUpstreamSystemPrefix...), client...)
	if len(system) == 3 {
		return system, nil
	}
	texts := []string{}
	var control any
	for _, value := range system[2:] {
		block := obj(value)
		if str(block["type"]) != "text" {
			return nil, invalid("系统前缀之后仅支持普通 text 系统块")
		}
		for key := range block {
			if key != "type" && key != "text" && key != "cache_control" {
				return nil, invalid("系统块存在无法合并的字段")
			}
		}
		texts = append(texts, str(block["text"]))
		if block["cache_control"] != nil {
			if control != nil && js(control) != js(block["cache_control"]) {
				return nil, invalid("不能保留多个不同的 system 缓存策略")
			}
			control = block["cache_control"]
		}
	}
	last := M{"type": "text", "text": strings.Join(texts, "\n\n")}
	if control != nil {
		last["cache_control"] = control
	}
	return append(system[:2], last), nil
}
