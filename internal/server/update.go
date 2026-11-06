package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const updateRepository = "do-whilefor/zcode2api"
const updateArchiveLimit = 128 << 20
const updateBinaryLimit = 128 << 20

var updateVersionPattern = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:[-+][0-9A-Za-z.-]+)?$`)

type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type githubRelease struct {
	Tag         string         `json:"tag_name"`
	URL         string         `json:"html_url"`
	Body        string         `json:"body"`
	PublishedAt string         `json:"published_at"`
	Draft       bool           `json:"draft"`
	Prerelease  bool           `json:"prerelease"`
	Assets      []releaseAsset `json:"assets"`
}

type updateReleaseCache struct {
	Release   githubRelease `json:"release"`
	CheckedAt int64         `json:"checked_at"`
	ETag      string        `json:"etag"`
}

type updateState struct {
	Phase      string `json:"phase"`
	Version    string `json:"version,omitempty"`
	Message    string `json:"message,omitempty"`
	Downloaded int64  `json:"downloaded,omitempty"`
	Total      int64  `json:"total,omitempty"`
	StartedAt  int64  `json:"started_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

type updateManager struct {
	once     sync.Once
	mu       sync.Mutex
	checkMu  sync.Mutex
	cache    updateReleaseCache
	support  updateSupport
	running  bool
	checking bool
}

func (a *App) updateDirectory() string {
	directory, _ := filepath.Abs(filepath.Join(a.data, "updates"))
	return directory
}

func readUpdateJSON(filename string, target any) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(io.LimitReader(file, 2<<20)).Decode(target)
}

func writeUpdateJSON(filename string, value any) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(filename), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = json.NewEncoder(file).Encode(value); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), filename)
}

func (a *App) loadUpdater() {
	a.updates.once.Do(func() {
		a.updates.support = detectUpdateSupport(a.version)
		_ = readUpdateJSON(filepath.Join(a.updateDirectory(), "release.json"), &a.updates.cache)
		var state updateState
		filename := filepath.Join(a.updateDirectory(), "state.json")
		if readUpdateJSON(filename, &state) == nil && (state.Phase == "downloading" || state.Phase == "preparing") {
			state.Phase, state.Message, state.FinishedAt = "error", "服务已重启，上次更新已中断，可以重新检查更新", nowMS()
			_ = writeUpdateJSON(filename, state)
		}
	})
}

// Compare release numbers so a local hotfix never gets replaced by the same
// published base version. Only a newer stable release is offered.
func updateVersionNumbers(version string) ([3]uint64, bool) {
	var numbers [3]uint64
	parts := updateVersionPattern.FindStringSubmatch(version)
	if len(parts) != 4 {
		return numbers, false
	}
	for index := range numbers {
		n, err := strconv.ParseUint(parts[index+1], 10, 64)
		if err != nil {
			return numbers, false
		}
		numbers[index] = n
	}
	return numbers, true
}

func newerUpdate(current, latest string) bool {
	old, oldOK := updateVersionNumbers(current)
	next, nextOK := updateVersionNumbers(latest)
	if !oldOK || !nextOK {
		return false
	}
	for index := range old {
		if next[index] != old[index] {
			return next[index] > old[index]
		}
	}
	return false
}

func updateAssets(release githubRelease) (releaseAsset, releaseAsset, error) {
	name := fmt.Sprintf("zcode2api_%s_%s_%s.tar.gz", release.Tag, runtime.GOOS, runtime.GOARCH)
	var binary, checksums releaseAsset
	for _, asset := range release.Assets {
		switch asset.Name {
		case name:
			if binary.Name != "" {
				return binary, checksums, fmt.Errorf("发行包名称重复")
			}
			binary = asset
		case "SHA256SUMS":
			if checksums.Name != "" {
				return binary, checksums, fmt.Errorf("校验文件名称重复")
			}
			checksums = asset
		}
	}
	if binary.Name == "" || checksums.Name == "" || binary.Size <= 0 || binary.Size > updateArchiveLimit || checksums.Size > 1<<20 {
		return binary, checksums, fmt.Errorf("该版本缺少当前平台发行包或 SHA256SUMS")
	}
	for _, asset := range []releaseAsset{binary, checksums} {
		expected := "https://github.com/" + updateRepository + "/releases/download/" + release.Tag + "/" + asset.Name
		if asset.URL != expected {
			return binary, checksums, fmt.Errorf("发行包下载地址与官方仓库不一致")
		}
	}
	return binary, checksums, nil
}

func (a *App) updateStatus() M {
	a.loadUpdater()
	a.updates.mu.Lock()
	cache, support := a.updates.cache, a.updates.support
	running, checking := a.updates.running, a.updates.checking
	a.updates.mu.Unlock()
	state := updateState{Phase: "idle"}
	_ = readUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), &state)
	if !running && updateBusy(state.Phase) && state.StartedAt < nowMS()-int64((20*time.Minute)/time.Millisecond) {
		state.Phase, state.Message, state.FinishedAt = "error", "更新任务已超时，请检查服务状态后重试", nowMS()
		_ = writeUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), state)
	}
	available := newerUpdate(a.version, cache.Release.Tag)
	assetOK, assetReason := false, ""
	if cache.Release.Tag != "" {
		_, _, err := updateAssets(cache.Release)
		assetOK = err == nil
		if err != nil {
			assetReason = err.Error()
		}
	}
	return M{"current_version": a.version, "latest_version": cache.Release.Tag, "repository": updateRepository,
		"repository_url": "https://github.com/" + updateRepository, "release_url": cache.Release.URL,
		"release_notes": cache.Release.Body, "published_at": cache.Release.PublishedAt, "checked_at": cache.CheckedAt,
		"available": available, "supported": support.Supported, "reason": support.Reason, "deployment": support.Mode,
		"asset_ready": assetOK, "asset_reason": assetReason, "busy": running || updateBusy(state.Phase), "checking": checking, "state": state}
}

func updateBusy(phase string) bool {
	return phase == "downloading" || phase == "preparing" || phase == "applying" || phase == "restarting"
}

func updateRedirect(request *http.Request, via []*http.Request) error {
	host := request.URL.Hostname()
	if len(via) > 5 || request.URL.Scheme != "https" || request.URL.User != nil || request.URL.Port() != "" ||
		(host != "api.github.com" && host != "github.com" && host != "release-assets.githubusercontent.com" && host != "objects.githubusercontent.com") {
		return fmt.Errorf("更新下载重定向地址无效")
	}
	return nil
}

func (a *App) updateClient(ctx context.Context) (*http.Client, func(), error) {
	ctx, release, err := a.pinOutboundProxy(ctx, nil)
	if err != nil {
		return nil, func() {}, fmt.Errorf("更新网络连接不可用：%w", err)
	}
	client, err := a.client(ctx)
	if err != nil {
		release()
		return nil, func() {}, err
	}
	return &http.Client{Transport: client.Transport, CheckRedirect: updateRedirect}, release, nil
}

func updateGET(ctx context.Context, client *http.Client, address, etag string) (*http.Response, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || (u.Host != "api.github.com" && u.Host != "github.com") {
		return nil, fmt.Errorf("更新地址无效")
	}
	request, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "zcode2api-updater")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("无法连接 GitHub，请检查服务器网络或出站代理")
	}
	if response.StatusCode != 200 && response.StatusCode != 304 {
		response.Body.Close()
		switch response.StatusCode {
		case 403, 429:
			return nil, fmt.Errorf("GitHub 请求频率受限，请稍后重试")
		case 404:
			return nil, fmt.Errorf("仓库尚未发布正式版本或发行文件不存在")
		default:
			return nil, fmt.Errorf("GitHub 返回 HTTP %d", response.StatusCode)
		}
	}
	return response, nil
}

func fetchUpdateRelease(ctx context.Context, client *http.Client, cache updateReleaseCache) (updateReleaseCache, error) {
	response, err := updateGET(ctx, client, "https://api.github.com/repos/"+updateRepository+"/releases/latest", cache.ETag)
	if err != nil {
		return cache, err
	}
	defer response.Body.Close()
	if response.StatusCode == 304 {
		if cache.Release.Tag == "" {
			return cache, fmt.Errorf("GitHub 版本缓存无效，请重新检查")
		}
		cache.CheckedAt = nowMS()
		return cache, nil
	}
	var release githubRelease
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release); err != nil {
		return cache, fmt.Errorf("GitHub 版本信息格式无效")
	}
	if _, valid := updateVersionNumbers(release.Tag); !valid || strings.ContainsAny(release.Tag, "-+") || release.Draft || release.Prerelease ||
		release.URL != "https://github.com/"+updateRepository+"/releases/tag/"+release.Tag {
		return cache, fmt.Errorf("GitHub 未返回有效的正式版本")
	}
	return updateReleaseCache{Release: release, CheckedAt: nowMS(), ETag: response.Header.Get("ETag")}, nil
}

func (a *App) checkUpdate(parent context.Context) error {
	a.loadUpdater()
	if !a.updates.checkMu.TryLock() {
		return fmt.Errorf("正在检查更新，请稍候")
	}
	defer a.updates.checkMu.Unlock()
	a.updates.mu.Lock()
	cache := a.updates.cache
	if a.updates.running {
		a.updates.mu.Unlock()
		return fmt.Errorf("更新正在进行中")
	}
	if cache.CheckedAt > nowMS()-10000 {
		a.updates.mu.Unlock()
		return nil
	}
	a.updates.checking = true
	a.updates.mu.Unlock()
	defer func() { a.updates.mu.Lock(); a.updates.checking = false; a.updates.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	client, release, err := a.updateClient(ctx)
	if err != nil {
		return err
	}
	defer release()
	cache, err = fetchUpdateRelease(ctx, client, cache)
	if err != nil {
		return err
	}
	if err = writeUpdateJSON(filepath.Join(a.updateDirectory(), "release.json"), cache); err != nil {
		return fmt.Errorf("无法保存版本信息，请检查数据目录权限")
	}
	a.updates.mu.Lock()
	a.updates.cache = cache
	a.updates.mu.Unlock()
	return nil
}

func (a *App) startUpdate(version string) error {
	a.loadUpdater()
	a.updates.mu.Lock()
	defer a.updates.mu.Unlock()
	if a.updates.running || a.updates.checking {
		return fmt.Errorf("已有更新或版本检查正在进行")
	}
	var previous updateState
	_ = readUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), &previous)
	if updateBusy(previous.Phase) {
		return fmt.Errorf("已有更新正在进行")
	}
	if !a.updates.support.Supported {
		return fmt.Errorf("%s", a.updates.support.Reason)
	}
	cache := a.updates.cache
	if version == "" || version != cache.Release.Tag || cache.CheckedAt < nowMS()-int64((30*time.Minute)/time.Millisecond) {
		return fmt.Errorf("请先检查最新版本，再提交更新")
	}
	if !newerUpdate(a.version, version) {
		return fmt.Errorf("当前已是最新发行版本，不执行降级或覆盖本地修复版")
	}
	if _, _, err := updateAssets(cache.Release); err != nil {
		return err
	}
	state := updateState{Phase: "downloading", Version: version, Message: "正在下载发行包", StartedAt: nowMS()}
	if err := writeUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), state); err != nil {
		return fmt.Errorf("无法创建更新任务，请检查数据目录权限")
	}
	a.updates.running = true
	a.background(func() { a.runUpdate(cache, state) })
	return nil
}

func (a *App) runUpdate(cache updateReleaseCache, state updateState) {
	defer func() { a.updates.mu.Lock(); a.updates.running = false; a.updates.mu.Unlock() }()
	stateFile := filepath.Join(a.updateDirectory(), "state.json")
	err := a.prepareUpdate(cache, &state)
	if err != nil {
		state.Phase, state.Message, state.FinishedAt = "error", err.Error(), nowMS()
		_ = writeUpdateJSON(stateFile, state)
		a.log.Printf("更新失败: %v", err)
		a.audit("system", "update.failed", M{"version": state.Version, "error": state.Message}, "")
	}
}

func (a *App) prepareUpdate(cache updateReleaseCache, state *updateState) error {
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	client, release, err := a.updateClient(ctx)
	if err != nil {
		return err
	}
	defer release()
	fresh, err := fetchUpdateRelease(ctx, client, cache)
	if err != nil {
		return err
	}
	if fresh.Release.Tag != state.Version {
		return fmt.Errorf("最新版本已变化，请重新检查后更新")
	}
	asset, checksumAsset, err := updateAssets(fresh.Release)
	if err != nil {
		return err
	}
	response, err := updateGET(ctx, client, checksumAsset.URL, "")
	if err != nil {
		return err
	}
	checksums, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	response.Body.Close()
	if err != nil || len(checksums) > 1<<20 {
		return fmt.Errorf("无法读取发行包校验文件")
	}
	expected := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(strings.TrimPrefix(fields[1], "*"), "./") == asset.Name {
			if expected != "" {
				return fmt.Errorf("发行包校验记录重复")
			}
			expected = strings.ToLower(fields[0])
		}
	}
	if decoded, err := hex.DecodeString(expected); err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("发行包缺少有效的 SHA256 校验值")
	}
	if asset.Digest != "" && asset.Digest != "sha256:"+expected {
		return fmt.Errorf("GitHub 摘要与 SHA256SUMS 不一致")
	}
	job, err := os.MkdirTemp(a.updateDirectory(), "job-*")
	if err != nil {
		return fmt.Errorf("无法创建更新目录")
	}
	archive, err := os.OpenFile(filepath.Join(job, "release.tar.gz"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	response, err = updateGET(ctx, client, asset.URL, "")
	if err != nil {
		archive.Close()
		return err
	}
	state.Total = asset.Size
	lastProgress := time.Time{}
	progress := &updateProgress{write: func(count int64) {
		state.Downloaded = count
		if time.Since(lastProgress) >= 500*time.Millisecond {
			lastProgress = time.Now()
			_ = writeUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), state)
		}
	}}
	digest := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(archive, digest, progress), io.LimitReader(response.Body, updateArchiveLimit+1))
	response.Body.Close()
	closeErr := archive.Close()
	if copyErr != nil || closeErr != nil || size != asset.Size || size > updateArchiveLimit {
		return fmt.Errorf("发行包下载不完整，请检查服务器网络后重试")
	}
	if hex.EncodeToString(digest.Sum(nil)) != expected {
		return fmt.Errorf("发行包 SHA256 校验失败，未替换当前程序")
	}
	state.Phase, state.Message = "preparing", "校验通过，正在准备备份"
	if err = writeUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), state); err != nil {
		return err
	}
	binary := filepath.Join(job, "new-binary")
	if err = extractUpdateBinary(archive.Name(), asset.Name, binary); err != nil {
		return err
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
	command := exec.CommandContext(probeCtx, binary, "--version")
	output, err := command.Output()
	probeCancel()
	if err != nil || strings.TrimSpace(string(output)) != "zcode2api "+state.Version {
		return fmt.Errorf("发行包内程序版本不匹配或无法运行")
	}
	return a.launchUpdateHelper(ctx, job, binary, state)
}

type updateProgress struct {
	count int64
	write func(int64)
}

func (progress *updateProgress) Write(data []byte) (int, error) {
	progress.count += int64(len(data))
	progress.write(progress.count)
	return len(data), nil
}

func extractUpdateBinary(archiveName, assetName, output string) error {
	archive, err := os.Open(archiveName)
	if err != nil {
		return err
	}
	defer archive.Close()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("发行包压缩格式无效")
	}
	defer compressed.Close()
	reader := tar.NewReader(io.LimitReader(compressed, 256<<20))
	expected := strings.TrimSuffix(assetName, ".tar.gz") + "/zcode2api"
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("发行包内容无效")
		}
		if header.Name != expected {
			continue
		}
		if found || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > updateBinaryLimit {
			return fmt.Errorf("发行包内的可执行文件无效或重复")
		}
		found = true
		file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			return err
		}
		n, err := io.Copy(file, reader)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil || n != header.Size {
			return fmt.Errorf("发行包内程序解压不完整")
		}
	}
	if !found {
		return fmt.Errorf("发行包中未找到当前平台程序")
	}
	return nil
}
