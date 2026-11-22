package server

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"

	"zcode2api/internal/server/shared"
)

func parseAdminName(value any, label string) (string, error) {
	name, _ := value.(string)
	name = strings.TrimSpace(name)
	if name == "" || len(utf16.Encode([]rune(name))) > 80 {
		return "", fmt.Errorf("请输入%s，最多 80 个字符", label)
	}
	return name, nil
}

func parseEnabledFlag(value any) (int, error) {
	switch shared.Str(value) {
	case "true", "1":
		return 1, nil
	case "false", "0":
		return 0, nil
	default:
		return 0, errors.New("启用状态无效")
	}
}
