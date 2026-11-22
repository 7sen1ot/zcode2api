package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
	"zcode2api/internal/server/shared"
)

// DownloadUpdateArchive starts each attempt with an empty file and a new hash.
// Only a completely verified archive is handed to the installer.
func DownloadUpdateArchive(ctx context.Context, client *http.Client, asset releaseAsset, mirror, expected, filename string, progress func(string, int64)) error {
	sources := []string{""}
	if mirror != "" {
		sources = []string{mirror, ""}
	}
	var lastErr error
	for _, source := range sources {
		label := "GitHub"
		if source != "" {
			label = "镜像"
		}
		for attempt := 0; attempt < 2; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			message := "正在从" + label + "下载发行包"
			if attempt > 0 {
				message = "下载中断，正在从" + label + "重试"
				if err := shared.Pause(ctx, time.Second); err != nil {
					return err
				}
			} else if lastErr != nil {
				message = "镜像下载失败，正在从 GitHub 重新下载"
			}
			progress(message, 0)
			retry, err := downloadArchiveAttempt(ctx, client, asset, source, expected, filename, func(count int64) {
				progress(message, count)
			})
			if err == nil || !retry {
				return err
			}
			lastErr = err
			var status *updateStatusError
			if errors.As(err, &status) && status.status < 500 && status.status != http.StatusRequestTimeout {
				break
			}
		}
	}
	return lastErr
}

func downloadArchiveAttempt(ctx context.Context, client *http.Client, asset releaseAsset, source, expected, filename string, progress func(int64)) (retry bool, err error) {
	// A stalled mirror must leave time for fallback. Slow downloads keep their
	// budget while bytes are arriving, up to the installer's overall deadline.
	attempt, cancel := context.WithCancel(ctx)
	defer cancel()
	const idleTimeout = 45 * time.Second
	timer := time.AfterFunc(idleTimeout, cancel)
	defer timer.Stop()
	response, err := UpdateGET(attempt, client, asset.URL, "", source)
	if err != nil {
		return true, err
	}
	defer response.Body.Close()
	archive, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := archive.Close(); closeErr != nil {
			retry, err = false, fmt.Errorf("发行包写入失败：%w", closeErr)
		}
		if err != nil {
			if removeErr := os.Remove(filename); removeErr != nil {
				retry, err = false, fmt.Errorf("无法清理未完成的下载：%w", removeErr)
			}
		}
	}()
	digest := sha256.New()
	counter := &UpdateProgress{OnProgress: func(count int64) {
		timer.Reset(idleTimeout)
		progress(count)
	}}
	size, err := io.Copy(io.MultiWriter(archive, digest, counter), io.LimitReader(response.Body, asset.Size+1))
	var diskErr *os.PathError
	if errors.As(err, &diskErr) {
		return false, fmt.Errorf("发行包写入失败：%w", err)
	}
	if err != nil || size < asset.Size {
		return true, fmt.Errorf("发行包下载中断（已收到 %d / %d 字节）", size, asset.Size)
	}
	if size != asset.Size || hex.EncodeToString(digest.Sum(nil)) != expected {
		return false, fmt.Errorf("发行包大小或 SHA256 校验失败，已取消安装")
	}
	return false, nil
}
