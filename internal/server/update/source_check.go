package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"zcode2api/internal/server/process"
	"zcode2api/internal/server/shared"
)

var sourceCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

type SourceCheck struct {
	LocalCommit  string `json:"local_commit,omitempty"`
	RemoteCommit string `json:"remote_commit"`
	Upstream     string `json:"upstream"`
	Relation     string `json:"relation"`
}

func SourceCheckout(root string) string {
	root, _ = filepath.Abs(root)
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	cwd, _ := os.Getwd()
	for _, dir := range []string{root, cwd, filepath.Dir(exe), filepath.Dir(filepath.Dir(exe))} {
		if !filepath.IsAbs(dir) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			continue
		}
		mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.TrimSpace(strings.SplitN(string(mod), "\n", 2)[0]) == "module zcode2api" {
			return dir
		}
	}
	return ""
}

func sourceGit(ctx context.Context, root string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{
		"-c", "core.hooksPath=" + os.DevNull, "-c", "submodule.recurse=false", "-C", root,
	}, args...)...)
	env := os.Environ()
	command.Env = env[:0]
	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		switch strings.ToUpper(key) {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_PREFIX":
			continue
		}
		command.Env = append(command.Env, value)
	}
	command.Env = append(command.Env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never", "GIT_NO_REPLACE_OBJECTS=1", "GIT_ALLOW_PROTOCOL=https:http:ssh:git")
	process.HideChildWindow(command)
	output, err := command.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return strings.TrimSpace(string(output)), err
}

func CheckSourceCheckout(ctx context.Context, root string) (*SourceCheck, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("检查源码更新需要服务端安装 Git")
	}
	local, err := sourceGit(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil || !sourceCommit.MatchString(local) {
		return nil, fmt.Errorf("无法读取本地源码提交，请检查 Git 工作区")
	}
	branch, err := sourceGit(ctx, root, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("当前源码处于分离 HEAD 状态，请切换到跟踪上游的分支")
	}
	tracking, err := sourceGit(ctx, root, "for-each-ref", "--format=%(upstream:remotename)%00%(upstream:remoteref)", branch)
	remote, ref, ok := strings.Cut(tracking, "\x00")
	if err != nil || !ok || remote == "" || remote == "." || !strings.HasPrefix(ref, "refs/heads/") {
		return nil, fmt.Errorf("当前分支没有配置远端跟踪分支")
	}
	result := &SourceCheck{LocalCommit: local, Upstream: remote + "/" + strings.TrimPrefix(ref, "refs/heads/")}
	// Use a private ref and suppress the configured refspec so checking never
	// moves local branches, remote-tracking refs, or FETCH_HEAD.
	pin := "refs/zcode2api/check-" + shared.Random(12)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = sourceGit(cleanup, root, "update-ref", "-d", pin)
	}()
	if _, err = sourceGit(ctx, root, "fetch", "--no-tags", "--no-prune", "--no-auto-gc", "--no-write-fetch-head", "--no-recurse-submodules", "--refmap=", "--", remote, ref+":"+pin); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("检查远端提交超时，请检查服务端 Git 网络配置")
		}
		return nil, fmt.Errorf("无法读取远端分支 %s，请检查服务端 Git 网络配置和仓库访问权限", result.Upstream)
	}
	result.RemoteCommit, err = sourceGit(ctx, root, "rev-parse", "--verify", pin+"^{commit}")
	if err != nil || !sourceCommit.MatchString(result.RemoteCommit) {
		return nil, fmt.Errorf("无法读取远端源码提交")
	}
	if local == result.RemoteCommit {
		result.Relation = "current"
		return result, nil
	}
	behind, err := sourceAncestor(ctx, root, local, result.RemoteCommit)
	if err != nil {
		return nil, err
	}
	if behind {
		result.Relation = "behind"
		return result, nil
	}
	ahead, err := sourceAncestor(ctx, root, result.RemoteCommit, local)
	if err != nil {
		return nil, err
	}
	if ahead {
		result.Relation = "ahead"
		return result, nil
	}
	shallow, err := sourceGit(ctx, root, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return nil, fmt.Errorf("无法确认源码工作区的提交历史")
	}
	result.Relation = "diverged"
	if shallow == "true" {
		result.Relation = "unknown"
	}
	return result, nil
}

func sourceAncestor(ctx context.Context, root, older, newer string) (bool, error) {
	_, err := sourceGit(ctx, root, "merge-base", "--is-ancestor", older, newer)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("无法比较本地与远端的提交历史")
}

func CheckGitHubSource(ctx context.Context, client *http.Client) (*SourceCheck, error) {
	response, err := UpdateGET(ctx, client, "https://api.github.com/repos/"+UpdateRepository+"/commits?per_page=1", "", "")
	if err != nil {
		return nil, fmt.Errorf("无法查询 GitHub 默认分支的提交：%w", err)
	}
	defer response.Body.Close()
	var commits []struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&commits); err != nil ||
		len(commits) != 1 || !sourceCommit.MatchString(commits[0].SHA) {
		return nil, fmt.Errorf("GitHub 未返回有效的源码提交")
	}
	result := &SourceCheck{RemoteCommit: commits[0].SHA, Upstream: "GitHub 默认分支", Relation: "unknown"}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && sourceCommit.MatchString(setting.Value) {
				result.LocalCommit = setting.Value
				break
			}
		}
	}
	if result.LocalCommit == result.RemoteCommit {
		result.Relation = "current"
	}
	return result, nil
}
