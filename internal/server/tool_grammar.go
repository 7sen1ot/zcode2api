package server

import (
	"context"
	_ "embed"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed grammar_check.py
var grammarCheckScript string

var grammarSlots = make(chan struct{}, 4)

type toolGrammar struct {
	Syntax, Definition, Python string
}

func grammarPython(root string) (string, error) {
	if configured := os.Getenv("ZCODE2API_GRAMMAR_PYTHON"); configured != "" {
		return exec.LookPath(configured)
	}
	venv := filepath.Join(root, "grammar_python", ".venv", "bin", "python")
	if runtime.GOOS == "windows" {
		venv = filepath.Join(root, "grammar_python", ".venv", "Scripts", "python.exe")
	}
	for _, candidate := range []string{venv, "python3", "python"} {
		if binary, e := exec.LookPath(candidate); e == nil {
			return binary, nil
		}
	}
	return "", &Failure{Kind: "exhausted", Code: "grammar_runtime_unavailable", Message: "grammar 校验需要 Python 3.10+ 和 llguidance；请安装 grammar_python/requirements.txt"}
}

func newToolGrammar(ctx context.Context, root string, format M) (*toolGrammar, error) {
	syntax, definition := str(format["syntax"]), str(format["definition"])
	if (syntax != "lark" && syntax != "regex") || definition == "" || len(definition) > 64<<10 {
		return nil, invalid("grammar 必须使用 lark 或 regex，且定义不超过 64 KiB")
	}
	python, e := grammarPython(root)
	if e != nil {
		return nil, &Failure{Kind: "exhausted", Code: "grammar_runtime_unavailable", Message: "找不到 grammar 校验的 Python；请检查 ZCODE2API_GRAMMAR_PYTHON 和 grammar_python/.venv"}
	}
	g := &toolGrammar{Syntax: syntax, Definition: definition, Python: python}
	if e := g.check(ctx, nil); e != nil {
		return nil, e
	}
	return g, nil
}

func (g *toolGrammar) check(parent context.Context, input *string) error {
	if input != nil && len(*input) > 1<<20 {
		return &Failure{Kind: "server", Code: "grammar_input_too_large", Message: "grammar 工具输入超过 1 MiB，已阻止返回工具调用"}
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	select {
	case grammarSlots <- struct{}{}:
		defer func() { <-grammarSlots }()
	case <-ctx.Done():
		return &Failure{Kind: "timeout", Code: "grammar_validation_timeout", Message: "等待 grammar 校验超时"}
	}
	request := M{"syntax": g.Syntax, "definition": g.Definition}
	if input != nil {
		request["input"] = *input
	}
	command := exec.CommandContext(ctx, g.Python, "-I", "-c", grammarCheckScript)
	hideChildWindow(command)
	command.Stdin = strings.NewReader(js(request))
	output := &limitedProcessOutput{}
	command.Stdout, command.Stderr = output, io.Discard
	command.WaitDelay = time.Second
	if e := command.Run(); e != nil {
		if ctx.Err() != nil {
			return &Failure{Kind: "timeout", Code: "grammar_validation_timeout", Message: "grammar 校验超时，已阻止返回工具调用"}
		}
		return &Failure{Kind: "exhausted", Code: "grammar_runtime_unavailable", Message: "grammar 校验器无法运行；请安装 grammar_python/requirements.txt"}
	}
	result := obj(parse(string(output.value), nil))
	if truth(result["ok"]) {
		return nil
	}
	switch str(result["code"]) {
	case "invalid_grammar":
		return &Failure{Kind: "invalid", Code: "invalid_grammar", Message: "grammar 无法完整校验：" + str(result["message"])}
	case "grammar_mismatch":
		return &Failure{Kind: "server", Code: "grammar_mismatch", Message: "上游 custom 工具输入不符合 grammar，已阻止返回该工具调用"}
	default:
		return &Failure{Kind: "exhausted", Code: "grammar_runtime_unavailable", Message: "grammar 校验器不可用；请安装 grammar_python/requirements.txt"}
	}
}
