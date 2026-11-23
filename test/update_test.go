package test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"zcode2api/internal/server/update"
)

type updateTransport func(*http.Request) (*http.Response, error)

func (send updateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return send(r) }

// The release packages use ZIP on Windows and a gzipped tarball on Unix.
func releaseArchive(t *testing.T, tag, content string) (string, []byte) {
	t.Helper()
	base := fmt.Sprintf("zcode2api_%s_%s_%s", tag, runtime.GOOS, runtime.GOARCH)
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		z := zip.NewWriter(&buf)
		file, err := z.Create(base + "/zcode2api.exe")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, content); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		return base + ".zip", buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tarball := tar.NewWriter(gz)
	header := &tar.Header{Name: base + "/zcode2api", Mode: 0700, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tarball.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tarball, content); err != nil {
		t.Fatal(err)
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return base + ".tar.gz", buf.Bytes()
}

func TestReleaseDownload(t *testing.T) {
	const tag, binary = "v1.10.0", "offline release fixture"
	name, archive := releaseArchive(t, tag, binary)
	repo := "https://github.com/" + update.UpdateRepository
	latest := "https://api.github.com/repos/" + update.UpdateRepository + "/releases/latest"
	assetBase := repo + "/releases/download/" + tag + "/"
	metadata := fmt.Sprintf(`{"tag_name":%q,"html_url":%q,"assets":[
		{"name":%q,"browser_download_url":%q,"size":%d},
		{"name":"SHA256SUMS","browser_download_url":%q,"size":100}]}`,
		tag, repo+"/releases/tag/"+tag, name, assetBase+name, len(archive), assetBase+"SHA256SUMS")
	const mirror = "https://mirror.invalid"
	const etag = `"offline-release"`
	var downloads []string
	servedArchive := archive
	client := &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
		res := &http.Response{StatusCode: 200, Header: make(http.Header), Body: http.NoBody, Request: r}
		switch r.URL.String() {
		case latest:
			if r.Header.Get("If-None-Match") == etag {
				res.StatusCode = 304
			} else {
				res.Header.Set("ETag", etag)
				res.Body = io.NopCloser(strings.NewReader(metadata))
			}
		case mirror + "/" + assetBase + name:
			downloads = append(downloads, r.URL.String())
			res.StatusCode = 404
		case assetBase + name:
			downloads = append(downloads, r.URL.String())
			res.Body = io.NopCloser(bytes.NewReader(servedArchive))
		default:
			return nil, fmt.Errorf("unexpected update request %s", r.URL)
		}
		return res, nil
	})}
	cache, err := update.FetchUpdateRelease(t.Context(), client, update.UpdateReleaseCache{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cache.Release.Tag != tag || cache.ETag != etag || !update.NewerUpdate("v1.9.9", cache.Release.Tag) || update.NewerUpdate(tag, tag) {
		t.Fatalf("unexpected release or version ordering for %q", cache.Release.Tag)
	}
	asset, sums, err := update.UpdateAssets(cache.Release)
	if err != nil || asset.Name != name || sums.Name != "SHA256SUMS" {
		t.Fatalf("platform asset selection: %q, %q, %v", asset.Name, sums.Name, err)
	}
	t.Run("revalidate cached release", func(t *testing.T) {
		// An unconditional request will now fail, so only a real 304 can reuse the cache.
		original := metadata
		metadata = `{}`
		defer func() { metadata = original }()
		previous := cache
		previous.CheckedAt = 1
		got, err := update.FetchUpdateRelease(t.Context(), client, previous, "")
		if err != nil || !reflect.DeepEqual(got.Release, cache.Release) || got.ETag != etag || got.CheckedAt <= previous.CheckedAt {
			t.Fatalf("conditional check lost the cached release or failed to refresh its timestamp: %v", err)
		}
	})
	t.Run("reject prerelease and retain last stable release", func(t *testing.T) {
		original := metadata
		metadata = fmt.Sprintf(`{"tag_name":"v2.0.0","html_url":%q,"prerelease":true}`, repo+"/releases/tag/v2.0.0")
		defer func() { metadata = original }()
		old := cache
		old.ETag = ""
		got, err := update.FetchUpdateRelease(t.Context(), client, old, "")
		if err == nil || !reflect.DeepEqual(got, old) {
			t.Fatal("a prerelease replaced the cached stable release or was accepted")
		}
	})
	digest := fmt.Sprintf("%x", sha256.Sum256(archive))
	t.Run("mirror fallback and extraction", func(t *testing.T) {
		dir := t.TempDir()
		filename := filepath.Join(dir, name)
		downloads = nil
		var count int64
		err := update.DownloadUpdateArchive(t.Context(), client, asset, mirror, digest, filename, func(_ string, n int64) { count = n })
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(downloads, []string{mirror + "/" + asset.URL, asset.URL}) || count != int64(len(archive)) {
			t.Fatalf("unexpected download path %v or final byte count %d", downloads, count)
		}
		out := filepath.Join(dir, "candidate")
		if err := update.ExtractUpdateBinary(filename, name, out); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(out)
		if err != nil || string(content) != binary {
			t.Fatalf("candidate differs from packaged file: %v", err)
		}
	})
	t.Run("reject corrupted download", func(t *testing.T) {
		servedArchive = bytes.Clone(archive)
		servedArchive[0] ^= 1
		defer func() { servedArchive = archive }()
		filename := filepath.Join(t.TempDir(), name)
		err := update.DownloadUpdateArchive(t.Context(), client, asset, "", digest, filename, func(string, int64) {})
		if err == nil {
			t.Fatal("accepted a same-size archive with an incorrect SHA-256 digest")
		}
		if _, err := os.Stat(filename); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("corrupted archive was not cleaned up: stat returned %v", err)
		}
	})
}

func TestInterruptedUpdateStatus(t *testing.T) {
	root := t.TempDir()
	stateFile := filepath.Join(root, "data", "updates", "state.json")
	state := update.UpdateState{Phase: "downloading", Version: "v1.10.0", Deployment: "standalone", StartedAt: time.Now().UnixMilli()}
	if err := update.WriteUpdateJSON(stateFile, state); err != nil {
		t.Fatal(err)
	}
	s := startService(t, root)
	raw := request(t, "GET", s.admin+"/api/settings/update", s.login(t), "", 200)
	status := decode[struct {
		Busy  bool
		State update.UpdateState
	}](t, raw)
	if status.Busy || status.State.Phase != "error" || status.State.Version != state.Version || status.State.FinishedAt < state.StartedAt {
		t.Fatalf("unexpected interrupted update status: %+v", status)
	}
	var persisted update.UpdateState
	if err := update.ReadUpdateJSON(stateFile, &persisted); err != nil || persisted != status.State {
		t.Fatalf("persisted update state differs from the API response: %+v, %v", persisted, err)
	}
}
