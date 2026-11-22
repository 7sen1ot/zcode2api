package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"zcode2api/internal/server/process"
	"zcode2api/internal/server/shared"
)

const (
	dockerRepository = "ghcr.io/7sen1ot/zcode2api"
	dockerService    = "zcode2api"
	dockerOverlay    = "compose.update.yaml"
	dockerPinFile    = ".zcode2api-docker.env"
	dockerJournal    = ".zcode2api-docker-update.json"
	dockerHeartbeat  = "docker-updater.json"
	dockerState      = "state.json"
	dockerImageEnv   = "ZCODE2API_DOCKER_IMAGE"
)

var (
	dockerID             = regexp.MustCompile(`^[0-9a-f]{64}$`)
	dockerDigest         = regexp.MustCompile(`^ghcr\.io/7sen1ot/zcode2api@sha256:[0-9a-f]{64}$`)
	errNoDockerContainer = errors.New("找不到 zcode2api Compose 容器")
)

func DockerReleaseImage(version string) string {
	if _, valid := updateVersionNumbers(version); !valid || strings.ContainsAny(version, "+-") {
		return ""
	}
	return dockerRepository + ":" + strings.TrimPrefix(version, "v")
}

func DockerUpdateSupport(data, version string) (bool, string) {
	if os.Getenv("ZCODE2API_DOCKER_UPDATES") != "1" || DockerReleaseImage(version) == "" {
		return false, "请使用官方镜像和 compose.update.yaml，并在宿主机启动更新器"
	}
	var heartbeat int64
	now := shared.NowMS()
	if ReadUpdateJSON(filepath.Join(data, "updates", dockerHeartbeat), &heartbeat) != nil || heartbeat < now-15000 || heartbeat > now+15000 {
		return false, "宿主机更新器未连接，请启动 zcode2api --docker-updater"
	}
	return true, ""
}

type dockerUpdater struct {
	dir, docker string
	updates     *os.Root
}

type dockerContainer struct {
	ID        string `json:"id"`
	ImageID   string `json:"image_id"`
	Reference string `json:"reference"`
	Files     string `json:"files"`
}

type dockerUpdatePlan struct {
	State    UpdateState `json:"state"`
	Previous string      `json:"previous_image"`
	Next     string      `json:"next_image"`
}

// RunDockerUpdater runs on the Compose host and accepts only version requests
// for this service. The gateway shares update state, with no Docker API access.
func RunDockerUpdater(ctx context.Context, dir string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for _, name := range []string{"compose.yaml", dockerOverlay} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("请在包含 compose.yaml 和 %s 的部署目录启动更新器", dockerOverlay)
		}
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("宿主机找不到 Docker CLI")
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".zcode2api-docker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := tryLockUpdateFile(lock); err != nil {
		return fmt.Errorf("该 Compose 目录已有更新器在运行")
	}
	data, err := os.OpenRoot(filepath.Join(dir, "data"))
	if err != nil {
		return fmt.Errorf("无法访问 Compose 部署的数据目录：%w", err)
	}
	defer data.Close()
	if err := data.MkdirAll("updates", 0700); err != nil {
		return err
	}
	updates, err := data.OpenRoot("updates")
	if err != nil {
		return err
	}
	defer updates.Close()
	runner := dockerUpdater{dir: dir, docker: docker, updates: updates}
	if err := runner.resume(ctx); err != nil {
		return err
	}
	preflight, cancel := context.WithTimeout(ctx, 15*time.Second)
	_, err = runner.currentContainer(preflight, true)
	cancel()
	if err != nil {
		return fmt.Errorf("请先用 compose.yaml 和 %s 启动官方镜像：%w", dockerOverlay, err)
	}
	heartbeat := func() error { return runner.writeState(dockerHeartbeat, shared.NowMS()) }
	if err := heartbeat(); err != nil {
		return err
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		for shared.Pause(heartbeatCtx, 4*time.Second) == nil {
			_ = heartbeat()
		}
	}()
	defer func() {
		stopHeartbeat()
		<-heartbeatDone
		_ = updates.Remove(dockerHeartbeat)
	}()
	fmt.Fprintln(os.Stderr, "Docker 更新器已连接，等待 WebUI 提交更新")
	for ctx.Err() == nil {
		var state UpdateState
		if err := runner.readState(dockerState, &state); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("无法读取更新请求：%w", err)
		}
		if state.Deployment == "container" && state.Phase == "queued" {
			if err := runner.update(ctx, state); err != nil {
				return err
			}
		}
		if err := shared.Pause(ctx, 2*time.Second); err != nil {
			break
		}
	}
	return nil
}

func (d dockerUpdater) readState(name string, target any) error {
	return readRootUpdateJSON(d.updates, name, target)
}

func (d dockerUpdater) writeState(name string, state any) error {
	return writeRootUpdateJSON(d.updates, name, state)
}

func (d dockerUpdater) newCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, d.docker, args...)
	cmd.Dir = d.dir
	process.HideChildWindow(cmd)
	return cmd
}

func (d dockerUpdater) command(ctx context.Context, args ...string) (string, error) {
	cmd := d.newCommand(ctx, args...)
	output, err := cmd.Output()
	return strings.TrimSpace(string(output)), err
}

func (d dockerUpdater) composeCommand(ctx context.Context, args ...string) *exec.Cmd {
	command := []string{"compose", "--project-directory", d.dir}
	for _, name := range []string{".env", dockerPinFile} {
		if _, err := os.Stat(filepath.Join(d.dir, name)); err == nil {
			command = append(command, "--env-file", name)
		}
	}
	command = append(command, "-f", "compose.yaml", "-f", dockerOverlay)
	return d.newCommand(ctx, append(command, args...)...)
}

func (d dockerUpdater) currentContainer(ctx context.Context, running bool) (dockerContainer, error) {
	var container dockerContainer
	args := []string{"ps", "--all", "-q", dockerService}
	if running {
		args = []string{"ps", "--status", "running", "-q", dockerService}
	}
	output, err := d.composeCommand(ctx, args...).Output()
	if err != nil {
		return container, fmt.Errorf("无法查询 zcode2api Compose 容器：%w", err)
	}
	id := strings.TrimSpace(string(output))
	if id == "" {
		return container, errNoDockerContainer
	}
	if !dockerID.MatchString(id) {
		return container, fmt.Errorf("在线更新适用于单容器 Compose 服务")
	}
	format := `{"id":{{json .Id}},"image_id":{{json .Image}},"reference":{{json .Config.Image}},"files":{{json (index .Config.Labels "com.docker.compose.project.config_files")}}}`
	raw, err := d.command(ctx, "container", "inspect", "--format", format, id)
	if err != nil || json.Unmarshal([]byte(raw), &container) != nil || container.ID != id ||
		!strings.HasPrefix(container.ImageID, "sha256:") || !dockerID.MatchString(strings.TrimPrefix(container.ImageID, "sha256:")) {
		return container, fmt.Errorf("无法读取当前容器")
	}
	files := strings.Split(container.Files, ",")
	if len(files) != 2 || !sameDockerPath(files[0], filepath.Join(d.dir, "compose.yaml")) ||
		!sameDockerPath(files[1], filepath.Join(d.dir, dockerOverlay)) {
		return container, fmt.Errorf("当前容器使用了不同的 Compose 文件，无法保留其部署配置")
	}
	if !strings.HasPrefix(container.Reference, dockerRepository+":") && !dockerDigest.MatchString(container.Reference) {
		return container, fmt.Errorf("在线更新适用于官方 GHCR 镜像")
	}
	return container, nil
}

func sameDockerPath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func (d dockerUpdater) imageDigest(ctx context.Context, ref string) (string, error) {
	output, err := d.command(ctx, "image", "inspect", "--format", "{{json .RepoDigests}}", ref)
	var digests []string
	if err == nil && json.Unmarshal([]byte(output), &digests) == nil {
		for _, digest := range digests {
			if dockerDigest.MatchString(digest) {
				return digest, nil
			}
		}
	}
	return "", fmt.Errorf("无法获得官方镜像的 SHA256 摘要")
}

func (d dockerUpdater) containerVersion(ctx context.Context, id string) string {
	version, err := d.command(ctx, "exec", id, "/usr/local/bin/zcode2api", "--version")
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(version, "zcode2api ")
}

func (d dockerUpdater) healthy(ctx context.Context, image, version string) bool {
	container, err := d.currentContainer(ctx, true)
	if err != nil {
		return false
	}
	imageID, err := d.command(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil || imageID != container.ImageID || d.containerVersion(ctx, container.ID) != version {
		return false
	}
	_, err = d.command(ctx, "exec", container.ID, "/usr/local/bin/zcode2api", "--healthcheck", "auto")
	return err == nil
}

func (d dockerUpdater) pin(image string) error {
	if !dockerDigest.MatchString(image) {
		return fmt.Errorf("镜像摘要无效")
	}
	file, err := os.CreateTemp(d.dir, ".docker-pin-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = fmt.Fprintln(file, dockerImageEnv+"="+image)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return replaceUpdateState(func() error { return os.Rename(file.Name(), filepath.Join(d.dir, dockerPinFile)) })
}

func (d dockerUpdater) deploy(ctx context.Context, image, version string) error {
	if _, err := d.currentContainer(ctx, false); err != nil && !errors.Is(err, errNoDockerContainer) {
		return err
	}
	if err := d.pin(image); err != nil {
		return err
	}
	if d.healthy(ctx, image, version) {
		return nil
	}
	// Shell variables take precedence over Compose env files. Use the exact
	// digest here even when the invoking shell has a different image setting.
	cmd := d.composeCommand(ctx, "up", "-d", "--no-deps", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "90", dockerService)
	cmd.Env = append(os.Environ(), dockerImageEnv+"="+image)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Compose 容器重建或健康检查失败：%w", err)
	}
	if !d.healthy(ctx, image, version) {
		return fmt.Errorf("运行中的容器版本或健康检查不匹配")
	}
	return nil
}

func (d dockerUpdater) finish(state UpdateState, phase, message string) error {
	state.Phase, state.Message, state.FinishedAt = phase, message, shared.NowMS()
	return d.writeState(dockerState, state)
}

func (d dockerUpdater) update(ctx context.Context, state UpdateState) error {
	image := DockerReleaseImage(state.Version)
	if image == "" || !NewerUpdate(state.PreviousVersion, state.Version) ||
		state.StartedAt < shared.NowMS()-int64((20*time.Minute)/time.Millisecond) || state.StartedAt > shared.NowMS()+15000 {
		return d.finish(state, "error", "Docker 更新请求无效或已过期，请重新检查版本")
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	container, err := d.currentContainer(ctx, true)
	if err != nil || d.containerVersion(ctx, container.ID) != state.PreviousVersion {
		return d.finish(state, "error", "运行中的容器已变更，请重新检查版本")
	}
	previous, err := d.imageDigest(ctx, container.ImageID)
	if err != nil {
		return d.finish(state, "error", err.Error())
	}
	state.Phase, state.Message = "downloading", "宿主机正在拉取官方镜像"
	if err := d.writeState(dockerState, state); err != nil {
		return err
	}
	if _, err := d.command(ctx, "pull", image); err != nil {
		return d.finish(state, "error", "无法拉取官方镜像，请检查宿主机到 GHCR 的连接")
	}
	next, err := d.imageDigest(ctx, image)
	if err != nil {
		return d.finish(state, "error", err.Error())
	}
	version, err := d.command(ctx, "run", "--rm", "--network", "none", "--entrypoint", "/usr/local/bin/zcode2api", next, "--version")
	if err != nil || version != "zcode2api "+state.Version {
		return d.finish(state, "error", "镜像内程序版本与目标版本不一致")
	}
	plan := dockerUpdatePlan{State: state, Previous: previous, Next: next}
	if err := WriteUpdateJSON(filepath.Join(d.dir, dockerJournal), plan); err != nil {
		return d.finish(state, "error", "无法保存 Docker 回滚计划")
	}
	state.Phase, state.Message = "applying", "正在使用新镜像重建容器"
	if err := d.writeState(dockerState, state); err != nil {
		return err
	}
	if err := d.deploy(ctx, next, state.Version); err != nil {
		return d.rollback(plan, err.Error())
	}
	return d.complete(plan, "completed", "容器更新完成")
}

func (d dockerUpdater) complete(plan dockerUpdatePlan, phase, message string) error {
	if err := d.finish(plan.State, phase, message); err != nil {
		return err
	}
	return os.Remove(filepath.Join(d.dir, dockerJournal))
}

func (d dockerUpdater) rollback(plan dockerUpdatePlan, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := d.deploy(ctx, plan.Previous, plan.State.PreviousVersion); err != nil {
		message := "容器更新失败，自动恢复未完成：" + reason
		_ = d.finish(plan.State, "error", message)
		return fmt.Errorf("%s；恢复失败：%w", message, err)
	}
	return d.complete(plan, "rolled_back", "容器更新失败，已恢复原镜像："+reason)
}

func (d dockerUpdater) resume(ctx context.Context) error {
	var plan dockerUpdatePlan
	err := ReadUpdateJSON(filepath.Join(d.dir, dockerJournal), &plan)
	if errors.Is(err, os.ErrNotExist) {
		var state UpdateState
		// A pull interrupted before saving a rollback plan has left the container intact.
		if d.readState(dockerState, &state) == nil && state.Deployment == "container" && state.Phase == "downloading" {
			return d.finish(state, "error", "镜像拉取中断，请重新提交更新")
		}
		return nil
	}
	if err != nil || plan.State.Deployment != "container" || DockerReleaseImage(plan.State.Version) == "" ||
		!dockerDigest.MatchString(plan.Previous) || !dockerDigest.MatchString(plan.Next) ||
		!NewerUpdate(plan.State.PreviousVersion, plan.State.Version) {
		return fmt.Errorf("宿主机 Docker 回滚计划无效")
	}
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if d.healthy(check, plan.Next, plan.State.Version) {
		if err := d.pin(plan.Next); err != nil {
			return err
		}
		return d.complete(plan, "completed", "容器更新完成")
	}
	return d.rollback(plan, "宿主机更新器上次执行时中断")
}
