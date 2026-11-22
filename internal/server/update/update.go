package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"zcode2api/internal/server/shared"
)

const UpdateRepository = "7sen1ot/zcode2api"

const UpdateArchiveLimit = 128 << 20

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

type UpdateReleaseCache struct {
	Release   githubRelease `json:"release"`
	CheckedAt int64         `json:"checked_at"`
	ETag      string        `json:"etag"`
	Mirror    string        `json:"mirror,omitempty"`
}

type UpdateState struct {
	Phase           string `json:"phase"`
	Version         string `json:"version,omitempty"`
	Message         string `json:"message,omitempty"`
	Downloaded      int64  `json:"downloaded,omitempty"`
	Total           int64  `json:"total,omitempty"`
	StartedAt       int64  `json:"started_at,omitempty"`
	FinishedAt      int64  `json:"finished_at,omitempty"`
	Deployment      string `json:"deployment,omitempty"`
	PreviousVersion string `json:"previous_version,omitempty"`
	BackupJob       string `json:"backup_job,omitempty"`
}

func ReadUpdateJSON(filename string, target any) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(io.LimitReader(file, 2<<20)).Decode(target)
}

func WriteUpdateJSON(filename string, value any) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(filename), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := writeUpdateFile(file, value); err != nil {
		return err
	}
	return replaceUpdateState(func() error { return os.Rename(file.Name(), filename) })
}

func writeUpdateFile(file *os.File, value any) error {
	err := json.NewEncoder(file).Encode(value)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func readRootUpdateJSON(root *os.Root, name string, target any) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return fmt.Errorf("更新状态文件无效")
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(io.LimitReader(file, 2<<20)).Decode(target)
}

func writeRootUpdateJSON(root *os.Root, name string, value any) error {
	temporary := ".state-" + shared.Random(8)
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if err := writeUpdateFile(file, value); err != nil {
		return err
	}
	return replaceUpdateState(func() error { return root.Rename(temporary, name) })
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

func NewerUpdate(current, latest string) bool {
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

func UpdateAssets(release githubRelease) (releaseAsset, releaseAsset, error) {
	extension := ".tar.gz"
	if runtime.GOOS == "windows" {
		extension = ".zip"
	}
	name := fmt.Sprintf("zcode2api_%s_%s_%s%s", release.Tag, runtime.GOOS, runtime.GOARCH, extension)
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
	if binary.Name == "" || checksums.Name == "" || binary.Size <= 0 || binary.Size > UpdateArchiveLimit || checksums.Size > 1<<20 {
		return binary, checksums, fmt.Errorf("该版本缺少当前平台发行包或 SHA256SUMS")
	}
	for _, asset := range []releaseAsset{binary, checksums} {
		expected := "https://github.com/" + UpdateRepository + "/releases/download/" + release.Tag + "/" + asset.Name
		if asset.URL != expected {
			return binary, checksums, fmt.Errorf("发行包下载地址与官方仓库不一致")
		}
	}
	return binary, checksums, nil
}

func UpdateBusy(phase string) bool {
	return phase == "queued" || phase == "downloading" || phase == "preparing" || phase == "applying" || phase == "restarting"
}

func updateRedirect(request *http.Request, via []*http.Request, mirrorHost string) error {
	host := strings.ToLower(request.URL.Host)
	if len(via) > 5 || request.URL.Scheme != "https" || request.URL.User != nil || request.URL.Opaque != "" {
		return fmt.Errorf("更新下载重定向地址无效")
	}
	switch host {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return nil
	}
	if mirrorHost != "" && strings.EqualFold(host, mirrorHost) {
		return nil
	}
	// GH-Proxy serves both published preset domains.
	if (mirrorHost == "gh-proxy.org" || mirrorHost == "gh-proxy.com") && (host == "gh-proxy.org" || host == "gh-proxy.com") {
		return nil
	}
	return fmt.Errorf("更新下载重定向地址无效")
}

type updateStatusError struct {
	status  int
	message string
}

func (e *updateStatusError) Error() string { return e.message }

func UpdateGET(ctx context.Context, client *http.Client, address, etag, mirror string) (*http.Response, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Host != "api.github.com" && u.Host != "github.com") {
		return nil, fmt.Errorf("更新地址无效")
	}
	source := "GitHub"
	if mirror != "" {
		address = mirror + "/" + address
		source = "GitHub 镜像"
	}
	request, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, fmt.Errorf("%s 请求地址无效", source)
	}
	request.Header.Set("User-Agent", "zcode2api-updater")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	mirrorHost := ""
	if mirror != "" {
		mirrorHost = strings.ToLower(request.URL.Host)
	}
	routed := *client
	routed.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		return updateRedirect(request, via, mirrorHost)
	}
	response, err := routed.Do(request)
	if err != nil {
		return nil, fmt.Errorf("无法连接%s，请检查服务器网络或出站代理", source)
	}
	if response.StatusCode != 200 && (response.StatusCode != 304 || etag == "") {
		response.Body.Close()
		message := fmt.Sprintf("%s返回 HTTP %d", source, response.StatusCode)
		switch response.StatusCode {
		case 403, 429:
			message = fmt.Sprintf("%s请求受限，请稍后重试", source)
		case 404:
			message = fmt.Sprintf("%s未找到正式版本或发行文件", source)
		}
		return nil, &updateStatusError{response.StatusCode, message}
	}
	return response, nil
}

func FetchUpdateRelease(ctx context.Context, client *http.Client, cache UpdateReleaseCache, mirror string) (UpdateReleaseCache, error) {
	etag := ""
	if cache.Mirror == mirror {
		etag = cache.ETag
	}
	response, err := UpdateGET(ctx, client, "https://api.github.com/repos/"+UpdateRepository+"/releases/latest", etag, mirror)
	if err != nil {
		return cache, err
	}
	defer response.Body.Close()
	if response.StatusCode == 304 {
		if cache.Release.Tag == "" {
			return cache, fmt.Errorf("GitHub 版本缓存无效，请重新检查")
		}
		cache.CheckedAt = shared.NowMS()
		return cache, nil
	}
	var release githubRelease
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release); err != nil {
		return cache, fmt.Errorf("GitHub 版本信息格式无效")
	}
	if _, valid := updateVersionNumbers(release.Tag); !valid || strings.ContainsAny(release.Tag, "-+") || release.Draft || release.Prerelease ||
		release.URL != "https://github.com/"+UpdateRepository+"/releases/tag/"+release.Tag {
		return cache, fmt.Errorf("GitHub 未返回有效的正式版本")
	}
	return UpdateReleaseCache{Release: release, CheckedAt: shared.NowMS(), ETag: response.Header.Get("ETag"), Mirror: mirror}, nil
}

type UpdateProgress struct {
	count      int64
	OnProgress func(int64) `json:"-"`
}

func (progress *UpdateProgress) Write(data []byte) (int, error) {
	progress.count += int64(len(data))
	progress.OnProgress(progress.count)
	return len(data), nil
}

func ExtractUpdateBinary(archiveName, assetName, output string) error {
	if strings.HasSuffix(assetName, ".zip") {
		return extractWindowsUpdateBinary(archiveName, strings.TrimSuffix(assetName, ".zip")+"/zcode2api.exe", output)
	}
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
		if err := writeUpdateBinary(output, reader, header.Size); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("发行包中未找到当前平台程序")
	}
	return nil
}

func extractWindowsUpdateBinary(archiveName, expected, output string) error {
	archive, err := zip.OpenReader(archiveName)
	if err != nil {
		if archive != nil {
			archive.Close()
		}
		return fmt.Errorf("Windows 发行包格式无效")
	}
	defer archive.Close()
	var candidate *zip.File
	for _, entry := range archive.File {
		if strings.ReplaceAll(entry.Name, "\\", "/") != expected {
			continue
		}
		if candidate != nil || !entry.Mode().IsRegular() || entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > updateBinaryLimit {
			return fmt.Errorf("发行包内的可执行文件无效或重复")
		}
		candidate = entry
	}
	if candidate == nil {
		return fmt.Errorf("发行包中未找到 Windows 程序")
	}
	reader, err := candidate.Open()
	if err != nil {
		return err
	}
	defer reader.Close()
	return writeUpdateBinary(output, reader, int64(candidate.UncompressedSize64))
}

func writeUpdateBinary(output string, reader io.Reader, size int64) error {
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	written, err := io.Copy(file, io.LimitReader(reader, size+1))
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil || written != size {
		return fmt.Errorf("发行包内程序解压不完整")
	}
	return nil
}
