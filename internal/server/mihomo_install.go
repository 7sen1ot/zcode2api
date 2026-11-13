package server

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const mihomoVersion = "v1.19.33"
const mihomoReleaseURL = "https://github.com/MetaCubeX/mihomo/releases/download/" + mihomoVersion + "/"
const mihomoArchiveLimit = 64 << 20
const mihomoBinaryLimit = 128 << 20

type mihomoAsset struct{ name, checksum string }

// Digests published by GitHub on the official v1.19.33 release assets.
var mihomoAssets = map[string]mihomoAsset{
	"windows/amd64": {"mihomo-windows-amd64-compatible-v1.19.33.zip", "7aaf636a4f2172dcd8ec34ac4f52797b059195e5afd6409508c963e545f8864d"},
	"windows/arm64": {"mihomo-windows-arm64-v1.19.33.zip", "a2a26869a14634c49f3fbcecc593b498ed8591fbdd7616b24475b3d60b148da3"},
	"linux/amd64":   {"mihomo-linux-amd64-compatible-v1.19.33.gz", "386508bfb1c72c43b6a0eda73708ecdce82546581b0ebcb16366c53757c622b6"},
	"linux/arm64":   {"mihomo-linux-arm64-v1.19.33.gz", "ba408b317080ee265c46cb923a50018c1b82cbd708551cdd46a1e3e307396187"},
	"darwin/amd64":  {"mihomo-darwin-amd64-compatible-v1.19.33.gz", "eefe967b74312f70b64efa0dfa54929d00c7af8e94416e1b12df09863a40f35b"},
	"darwin/arm64":  {"mihomo-darwin-arm64-v1.19.33.gz", "060b64b0f2c9a310831db0fe4adb2878de67bfa606c311813c1f13966aa504b6"},
}

func (a *App) managedMihomoPath() string {
	name := "mihomo"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(a.data, "tools", "mihomo", mihomoVersion, runtime.GOOS+"-"+runtime.GOARCH, name)
	if absolute, err := filepath.Abs(binary); err == nil {
		return absolute
	}
	return binary
}

func (a *App) resolveMihomo(configured string) (string, error) {
	if configured == "" || configured == "mihomo" {
		// Portable deployments may place Mihomo beside config.json.
		return resolveExecutable(a.root, "mihomo", a.managedMihomoPath(), filepath.Join(a.root, "mihomo"))
	}
	return resolveExecutable(a.root, configured)
}

func (a *App) mihomoStatus() M {
	return a.mihomoStatusForPath(str(obj(a.configValue("outbound_proxy"))["mihomo_path"]))
}

func (a *App) mihomoStatusForPath(configured string) M {
	_, supported := mihomoAssets[runtime.GOOS+"/"+runtime.GOARCH]
	binary, err := a.resolveMihomo(configured)
	return M{"installed": err == nil, "path": binary, "managed": binary == a.managedMihomoPath(), "version": mihomoVersion, "platform": runtime.GOOS + "/" + runtime.GOARCH, "supported": supported}
}

func mihomoRedirect(request *http.Request, via []*http.Request) error {
	host := request.URL.Hostname()
	if len(via) > 5 || request.URL.Scheme != "https" || request.URL.User != nil || (host != "github.com" && host != "release-assets.githubusercontent.com" && host != "objects.githubusercontent.com") {
		return fmt.Errorf("Mihomo 下载重定向被拒绝")
	}
	return nil
}

func (a *App) installMihomo(parent context.Context) (M, error) {
	asset, supported := mihomoAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if !supported {
		return nil, fmt.Errorf("当前平台暂不支持在线安装，请手动安装 Mihomo")
	}
	if !a.mihomoInstallMu.TryLock() {
		return nil, fmt.Errorf("Mihomo 正在安装，请稍后检查安装状态")
	}
	defer a.mihomoInstallMu.Unlock()
	ctx, cancel := a.operationContext(parent, 3*time.Minute)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Minute, CheckRedirect: mihomoRedirect}
	binary := a.managedMihomoPath()
	if _, err := exec.LookPath(binary); err != nil {
		if err = installMihomoAsset(ctx, client, asset, binary); err != nil {
			return nil, err
		}
	}
	return M{"ok": true, "installed": true, "path": binary, "version": mihomoVersion}, nil
}

func installMihomoAsset(ctx context.Context, client *http.Client, asset mihomoAsset, binary string) error {
	request, err := http.NewRequestWithContext(ctx, "GET", mihomoReleaseURL+asset.name, nil)
	if err != nil {
		return fmt.Errorf("Mihomo 下载地址无效")
	}
	request.Header.Set("User-Agent", "zcode2api-mihomo-installer")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("无法连接官方 GitHub 下载地址，请检查服务器网络或手动安装")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > mihomoArchiveLimit {
		return fmt.Errorf("Mihomo 下载响应无效或安装包超过 64 MB")
	}
	directory := filepath.Dir(binary)
	if err = os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("无法创建 Mihomo 安装目录，请检查数据目录写入权限")
	}
	archive, err := os.CreateTemp(directory, ".download-*")
	if err != nil {
		return fmt.Errorf("无法创建 Mihomo 下载文件")
	}
	defer os.Remove(archive.Name())
	digest := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(archive, digest), io.LimitReader(response.Body, mihomoArchiveLimit+1))
	closeErr := archive.Close()
	if copyErr != nil || closeErr != nil || size > mihomoArchiveLimit {
		return fmt.Errorf("Mihomo 安装包下载不完整或超过 64 MB")
	}
	if hex.EncodeToString(digest.Sum(nil)) != asset.checksum {
		return fmt.Errorf("Mihomo 安装包 SHA-256 校验失败，文件未安装")
	}
	output, err := os.CreateTemp(directory, ".binary-*")
	if err != nil {
		return fmt.Errorf("无法创建 Mihomo 可执行文件")
	}
	defer os.Remove(output.Name())
	err = extractMihomo(archive.Name(), asset.name, output)
	if err == nil {
		err = output.Sync()
	}
	closeErr = output.Close()
	if err != nil || closeErr != nil {
		return fmt.Errorf("Mihomo 安装包解压失败或超过 128 MB")
	}
	if err = os.Chmod(output.Name(), 0700); err != nil {
		return fmt.Errorf("无法设置 Mihomo 执行权限")
	}
	if ctx.Err() != nil {
		return fmt.Errorf("Mihomo 安装已取消或超时")
	}
	if err = os.Rename(output.Name(), binary); err != nil {
		return fmt.Errorf("无法保存 Mihomo，请检查文件占用及写入权限")
	}
	return nil
}

func extractMihomo(filename, assetName string, output io.Writer) error {
	var input io.ReadCloser
	if strings.HasSuffix(assetName, ".zip") {
		archive, err := zip.OpenReader(filename)
		if err != nil {
			return err
		}
		defer archive.Close()
		var executable *zip.File
		for _, file := range archive.File {
			base := path.Base(file.Name)
			if !file.FileInfo().IsDir() && strings.HasPrefix(base, "mihomo") && strings.HasSuffix(base, ".exe") {
				if executable != nil {
					return fmt.Errorf("安装包含多个可执行文件")
				}
				executable = file
			}
		}
		if executable == nil || executable.UncompressedSize64 > mihomoBinaryLimit {
			return fmt.Errorf("安装包缺少可执行文件或文件过大")
		}
		input, err = executable.Open()
		if err != nil {
			return err
		}
	} else {
		archive, err := os.Open(filename)
		if err != nil {
			return err
		}
		defer archive.Close()
		input, err = gzip.NewReader(archive)
		if err != nil {
			return err
		}
	}
	defer input.Close()
	size, err := io.Copy(output, io.LimitReader(input, mihomoBinaryLimit+1))
	if err != nil || size == 0 || size > mihomoBinaryLimit {
		return fmt.Errorf("可执行文件无效或过大")
	}
	return nil
}
