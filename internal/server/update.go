package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	settings "zcode2api/internal/server/config"
	"zcode2api/internal/server/process"
	"zcode2api/internal/server/shared"
	updater "zcode2api/internal/server/update"
)

type updateManager struct {
	once     sync.Once
	mu       sync.Mutex
	cache    updater.UpdateReleaseCache
	source   *updater.SourceCheck
	support  updater.UpdateSupport
	running  bool
	checking bool
}

func (a *App) updateDirectory() string {
	directory, _ := filepath.Abs(filepath.Join(a.data, "updates"))
	return directory
}

func (a *App) startUpdateBackupCleanup() {
	a.background(func() {
		directory := a.updateDirectory()
		stateFile := filepath.Join(directory, "state.json")
		lastCheck := time.Now()
		clean := func(now time.Time) {
			a.updates.mu.Lock()
			var state updater.UpdateState
			err := updater.ReadUpdateJSON(stateFile, &state)
			if err == nil {
				err = updater.RecordUpdateBackup(directory, state)
			}
			a.updates.mu.Unlock()
			if err != nil && !os.IsNotExist(err) {
				a.log.Printf("记录更新备份保留状态失败：%v", err)
			}
			if err := updater.CleanupUpdateBackups(directory, a.version, lastCheck, now); err != nil {
				a.log.Printf("清理更新备份失败：%v", err)
			}
			lastCheck = now
		}
		clean(lastCheck)
		ticker := time.NewTicker(updater.BackupCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				clean(time.Now())
			case <-a.ctx.Done():
				clean(time.Now())
				return
			}
		}
	})
}

func (a *App) loadUpdater() {
	a.updates.once.Do(func() {
		a.updates.support = updater.DetectUpdateSupport(a.version)
		if a.updates.support.CanCheck {
			_ = updater.ReadUpdateJSON(filepath.Join(a.updateDirectory(), "release.json"), &a.updates.cache)
			cached := a.updates.cache.Release
			if cached.URL != "https://github.com/"+updater.UpdateRepository+"/releases/tag/"+cached.Tag {
				a.updates.cache = updater.UpdateReleaseCache{}
			}
		}
		var state updater.UpdateState
		filename := filepath.Join(a.updateDirectory(), "state.json")
		if updater.ReadUpdateJSON(filename, &state) != nil {
			return
		}
		switch {
		case state.Deployment == "source" && updater.UpdateBusy(state.Phase):
			state.Message = "上次源码更新已中断，请核对当前程序与源码，已有备份可在数据目录的 updates 中查看"
		case state.Phase == "preparing" || state.Deployment != "container" && state.Phase == "downloading":
			state.Message = "服务已重启，上次更新已中断，可以重新检查更新"
		default:
			return
		}
		state.Phase, state.FinishedAt = "error", shared.NowMS()
		if err := updater.WriteUpdateJSON(filename, state); err != nil {
			a.log.Printf("记录更新中断状态失败：%v", err)
		}
	})
}

func (a *App) updateSupport() updater.UpdateSupport {
	a.loadUpdater()
	support := a.updates.support
	if support.Mode == "container" {
		support.Supported, support.Reason = updater.DockerUpdateSupport(a.data, a.version)
	}
	return support
}

func (a *App) updateStatus() shared.M {
	support := a.updateSupport()
	a.updates.mu.Lock()
	cache, source := a.updates.cache, a.updates.source
	running, checking := a.updates.running, a.updates.checking
	a.updates.mu.Unlock()
	state := updater.UpdateState{Phase: "idle"}
	_ = updater.ReadUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), &state)
	if !running && updater.UpdateBusy(state.Phase) && time.Since(time.UnixMilli(state.StartedAt)) > 20*time.Minute {
		state.Phase, state.Message, state.FinishedAt = "error", "更新任务已超时，请检查服务状态后重试", shared.NowMS()
		_ = updater.WriteUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), state)
	}
	available := updater.NewerUpdate(a.version, cache.Release.Tag) || source != nil && source.Relation == "behind"
	assetOK, assetReason := false, ""
	if cache.Release.Tag != "" {
		if support.Mode == "container" {
			assetOK = updater.DockerReleaseImage(cache.Release.Tag) != ""
		} else if _, _, err := updater.UpdateAssets(cache.Release); err != nil {
			assetReason = err.Error()
		} else {
			assetOK = true
		}
	}
	return shared.M{"current_version": a.version, "latest_version": cache.Release.Tag, "repository": updater.UpdateRepository,
		"repository_url": "https://github.com/" + updater.UpdateRepository, "release_url": cache.Release.URL,
		"release_notes": cache.Release.Body, "published_at": cache.Release.PublishedAt, "checked_at": cache.CheckedAt,
		"available": available, "can_check": support.CanCheck || support.SourceBuild, "supported": support.Supported, "reason": support.Reason, "deployment": support.Mode,
		"source_check": support.SourceBuild, "source": source,
		"asset_ready": assetOK, "asset_reason": assetReason, "busy": running || updater.UpdateBusy(state.Phase), "checking": checking, "state": state}
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
	return &http.Client{Transport: client.Transport}, release, nil
}

func (a *App) checkUpdate(parent context.Context) error {
	support := a.updateSupport()
	if !support.CanCheck && !support.SourceBuild {
		return fmt.Errorf("%s", support.Reason)
	}
	var mirror string
	if support.CanCheck {
		var err error
		mirror, err = settings.GitHubUpdateMirror(a.configValue("github_update_mirror"))
		if err != nil {
			return err
		}
	}
	a.updates.mu.Lock()
	if a.updates.checking {
		a.updates.mu.Unlock()
		return fmt.Errorf("正在检查更新，请稍候")
	}
	cache := a.updates.cache
	var active updater.UpdateState
	_ = updater.ReadUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), &active)
	if a.updates.running || updater.UpdateBusy(active.Phase) {
		a.updates.mu.Unlock()
		return fmt.Errorf("更新正在进行中")
	}
	if support.CanCheck && cache.Mirror == mirror && cache.CheckedAt > time.Now().Add(-10*time.Second).UnixMilli() {
		a.updates.mu.Unlock()
		return nil
	}
	a.updates.checking = true
	a.updates.mu.Unlock()
	defer func() { a.updates.mu.Lock(); a.updates.checking = false; a.updates.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	if support.SourceBuild {
		source, err := a.checkSource(ctx)
		if err != nil {
			return err
		}
		a.updates.mu.Lock()
		a.updates.source = source
		a.updates.mu.Unlock()
		return nil
	}
	client, release, err := a.updateClient(ctx)
	if err != nil {
		return err
	}
	defer release()
	cache, err = checkUpdateRelease(ctx, client, cache, mirror)
	if err != nil {
		return err
	}
	if err = updater.WriteUpdateJSON(filepath.Join(a.updateDirectory(), "release.json"), cache); err != nil {
		return fmt.Errorf("无法保存版本信息，请检查数据目录权限")
	}
	a.updates.mu.Lock()
	a.updates.cache = cache
	a.updates.mu.Unlock()
	return nil
}

func (a *App) checkSource(ctx context.Context) (*updater.SourceCheck, error) {
	if root := updater.SourceCheckout(a.root); root != "" {
		return updater.CheckSourceCheckout(ctx, root)
	}
	client, release, err := a.updateClient(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return updater.CheckGitHubSource(ctx, client, a.version)
}

func checkUpdateRelease(ctx context.Context, client *http.Client, cache updater.UpdateReleaseCache, mirror string) (updater.UpdateReleaseCache, error) {
	if mirror == "" {
		return updater.FetchUpdateRelease(ctx, client, cache, "")
	}
	probe, cancel := context.WithTimeout(ctx, 12*time.Second)
	release, mirrorErr := updater.FetchUpdateRelease(probe, client, cache, mirror)
	cancel()
	if mirrorErr == nil {
		return release, nil
	}
	release, err := updater.FetchUpdateRelease(ctx, client, cache, "")
	if err != nil {
		return cache, fmt.Errorf("镜像和直连均无法检查版本（%v，%v）", mirrorErr, err)
	}
	return release, nil
}

func (a *App) startUpdate(version string) error {
	support := a.updateSupport()
	mirror, err := settings.GitHubUpdateMirror(a.configValue("github_update_mirror"))
	if err != nil {
		return err
	}
	a.updates.mu.Lock()
	defer a.updates.mu.Unlock()
	if a.updates.running || a.updates.checking {
		return fmt.Errorf("已有更新或版本检查正在进行")
	}
	var previous updater.UpdateState
	_ = updater.ReadUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), &previous)
	if updater.UpdateBusy(previous.Phase) {
		return fmt.Errorf("已有更新正在进行")
	}
	if !support.Supported {
		return fmt.Errorf("%s", support.Reason)
	}
	cache := a.updates.cache
	if version == "" || cache.CheckedAt < shared.NowMS()-int64((30*time.Minute)/time.Millisecond) {
		return fmt.Errorf("请先检查最新版本，再提交更新")
	}
	if version != cache.Release.Tag || !updater.NewerUpdate(a.version, version) {
		return fmt.Errorf("目标版本已变化或无需升级，请重新检查")
	}
	state := updater.UpdateState{Phase: "downloading", Version: version, Message: "正在下载发行包", StartedAt: shared.NowMS(), Deployment: support.Mode, PreviousVersion: a.version}
	if support.Mode == "container" {
		if updater.DockerReleaseImage(version) == "" {
			return fmt.Errorf("该版本没有可用的容器镜像标签")
		}
		state.Phase, state.Message = "preparing", "正在备份数据库和配置"
	} else if _, _, err := updater.UpdateAssets(cache.Release); err != nil {
		return err
	}
	if err := updater.RecordUpdateBackup(a.updateDirectory(), previous); err != nil {
		a.log.Printf("记录更新备份保留状态失败：%v", err)
	}
	if err := updater.WriteUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), state); err != nil {
		return fmt.Errorf("无法创建更新任务，请检查数据目录权限")
	}
	a.updates.running = true
	a.background(func() { a.runUpdate(cache, state, mirror) })
	return nil
}

func (a *App) runUpdate(cache updater.UpdateReleaseCache, state updater.UpdateState, mirror string) {
	defer func() { a.updates.mu.Lock(); a.updates.running = false; a.updates.mu.Unlock() }()
	stateFile := filepath.Join(a.updateDirectory(), "state.json")
	var err error
	if state.Deployment == "container" {
		err = a.queueDockerUpdate(&state)
	} else {
		err = a.prepareUpdate(cache, &state, mirror)
	}
	if err != nil {
		state.Phase, state.Message, state.FinishedAt = "error", err.Error(), shared.NowMS()
		_ = updater.WriteUpdateJSON(stateFile, state)
		a.log.Printf("更新失败: %v", err)
	}
}

func (a *App) queueDockerUpdate(state *updater.UpdateState) error {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	job, err := os.MkdirTemp(a.updateDirectory(), "job-*")
	if err != nil {
		return err
	}
	if err := a.snapshotUpdate(ctx, job); err != nil {
		return err
	}
	state.BackupJob = filepath.Base(job)
	state.Phase, state.Message = "queued", "备份完成，等待宿主机更新器拉取镜像"
	return updater.WriteUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), state)
}

func (a *App) prepareUpdate(cache updater.UpdateReleaseCache, state *updater.UpdateState, mirror string) error {
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	client, release, err := a.updateClient(ctx)
	if err != nil {
		return err
	}
	defer release()
	metadata, cancelMetadata := ctx, func() {}
	if mirror != "" {
		metadata, cancelMetadata = context.WithTimeout(ctx, 12*time.Second)
	}
	fresh, err := updater.FetchUpdateRelease(metadata, client, cache, "")
	cancelMetadata()
	if err != nil {
		if mirror == "" {
			return err
		}
		// Cached mirror metadata can locate the archive; only the official
		// GitHub API or an official SHA256SUMS can supply its trusted hash.
		fresh = cache
	}
	if fresh.Release.Tag != state.Version {
		return fmt.Errorf("最新版本已变化，请重新检查后更新")
	}
	asset, checksumAsset, err := updater.UpdateAssets(fresh.Release)
	if err != nil {
		return err
	}
	officialDigest := ""
	if fresh.Mirror == "" {
		officialDigest = asset.Digest
	}
	var expected string
	if mirror != "" && officialDigest != "" {
		expected, _ = strings.CutPrefix(officialDigest, "sha256:")
	} else {
		checksumCtx, cancelChecksum := ctx, func() {}
		if mirror != "" {
			checksumCtx, cancelChecksum = context.WithTimeout(ctx, 25*time.Second)
		}
		expected, err = fetchOfficialUpdateChecksum(checksumCtx, client, checksumAsset.URL, asset.Name)
		cancelChecksum()
		if err != nil {
			if mirror != "" {
				return fmt.Errorf("无法从 GitHub 官方取得发行包校验信息，已取消更新（%w）", err)
			}
			return err
		}
	}
	if decoded, err := hex.DecodeString(expected); err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("发行包缺少有效的 SHA256 校验值")
	}
	if officialDigest != "" && officialDigest != "sha256:"+expected {
		return fmt.Errorf("GitHub 摘要与 SHA256SUMS 不一致")
	}
	job, err := os.MkdirTemp(a.updateDirectory(), "job-*")
	if err != nil {
		return fmt.Errorf("无法创建更新目录")
	}
	if err := updater.SecureUpdateJob(job); err != nil {
		return fmt.Errorf("无法保护更新目录：%w", err)
	}
	state.BackupJob = filepath.Base(job)
	archive := filepath.Join(job, asset.Name)
	state.Total = asset.Size
	lastProgress := time.Time{}
	err = updater.DownloadUpdateArchive(ctx, client, asset, mirror, expected, archive, func(message string, count int64) {
		state.Message, state.Downloaded = message, count
		if count == 0 || time.Since(lastProgress) >= 500*time.Millisecond {
			lastProgress = time.Now()
			_ = updater.WriteUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), state)
		}
	})
	if err != nil {
		return err
	}
	defer os.Remove(archive)
	state.Phase, state.Message = "preparing", "校验通过，正在准备备份"
	if err = updater.WriteUpdateJSON(filepath.Join(a.updateDirectory(), "state.json"), state); err != nil {
		return err
	}
	binary := filepath.Join(job, updater.UpdateBinaryFilename("new-binary"))
	if err = updater.ExtractUpdateBinary(archive, asset.Name, binary); err != nil {
		return err
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
	command := exec.CommandContext(probeCtx, binary, "--version")
	process.HideChildWindow(command)
	output, err := command.Output()
	probeCancel()
	if err != nil || strings.TrimSpace(string(output)) != "zcode2api "+state.Version {
		return fmt.Errorf("发行包内程序版本不匹配或无法运行")
	}
	return a.launchUpdateHelper(ctx, job, binary, state)
}

func fetchOfficialUpdateChecksum(ctx context.Context, client *http.Client, checksumURL, assetName string) (string, error) {
	response, err := updater.UpdateGET(ctx, client, checksumURL, "", "")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	checksums, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(checksums) > 1<<20 {
		return "", fmt.Errorf("无法读取发行包校验文件")
	}
	expected := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(strings.TrimPrefix(fields[1], "*"), "./") == assetName {
			if expected != "" {
				return "", fmt.Errorf("发行包校验记录重复")
			}
			expected = strings.ToLower(fields[0])
		}
	}
	return expected, nil
}

func (a *App) launchUpdateHelper(ctx context.Context, job, binary string, state *updater.UpdateState) error {
	support := updater.DetectUpdateSupport(a.version)
	if !support.Supported {
		return fmt.Errorf("%s", support.Reason)
	}
	data, err := filepath.Abs(a.data)
	if err != nil {
		return err
	}
	plan := updater.UpdatePlan{Service: support.Service, Binary: support.Binary, Candidate: binary,
		Backup: filepath.Join(job, updater.UpdateBinaryFilename("previous-binary")), Data: data, StateFile: filepath.Join(a.updateDirectory(), "state.json"),
		Version: state.Version, PreviousVersion: a.version, CreatedAt: shared.NowMS(), PID: os.Getpid(), WebPort: a.webPort, GatewayPort: a.gatewayPort,
		WebHost: a.webUpdateHost, GatewayHost: a.gatewayUpdateHost}
	if support.Service == "" {
		plan.Directory, err = os.Getwd()
		if err != nil {
			return err
		}
		plan.Args = os.Args[1:]
	}
	if err = updater.CopyUpdateFile(support.Binary, plan.Backup, 0700); err != nil {
		return fmt.Errorf("无法备份当前程序：%w", err)
	}
	plan.OldSHA256, err = updater.UpdateFileHash(plan.Backup)
	if err != nil {
		return err
	}
	plan.NewSHA256, err = updater.UpdateFileHash(binary)
	if err != nil {
		return err
	}
	if err := a.snapshotUpdate(ctx, job); err != nil {
		return err
	}
	helper := filepath.Join(job, updater.UpdateBinaryFilename("update-helper"))
	if err = updater.CopyUpdateFile(plan.Backup, helper, 0700); err != nil {
		return err
	}
	planFile := filepath.Join(job, "plan.json")
	if err = updater.WriteUpdateJSON(planFile, plan); err != nil {
		return err
	}
	state.Phase, state.Message = "applying", "备份完成，即将重启服务"
	if err = updater.WriteUpdateJSON(plan.StateFile, state); err != nil {
		return err
	}
	if support.Service == "" {
		if err := updater.StartStandaloneUpdater(ctx, helper, planFile); err != nil {
			return err
		}
		a.cancel()
		return nil
	}
	command := exec.CommandContext(ctx, support.SystemdRun, "--quiet", "--collect", "--unit=zcode2api-update-"+shared.Random(6),
		"--property=Type=exec", "--property=RuntimeMaxSec=10min", "--property=TimeoutStopSec=15s", "--", helper, "--apply-update", planFile)
	if err = command.Run(); err != nil {
		return fmt.Errorf("无法启动独立更新进程，当前程序未替换")
	}
	return nil
}

func (a *App) snapshotUpdate(ctx context.Context, job string) error {
	// SQLite creates a consistent backup while existing requests can finish.
	if _, err := a.db.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(job, "database-before.db")); err != nil {
		return fmt.Errorf("数据库备份失败，已取消更新")
	}
	if err := updater.CopyUpdateFile(filepath.Join(a.root, "config.json"), filepath.Join(job, "config-before.json"), 0600); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("配置文件备份失败，已取消更新")
	}
	return nil
}
