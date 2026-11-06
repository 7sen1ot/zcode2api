package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	"zcode2api/internal/server"
)

//go:embed src/web/*
var assets embed.FS

// version is set by release builds; app_version configures the upstream API.
var version = "dev"

func main() {
	root := flag.String("root", ".", "配置文件所在目录")
	health := flag.String("healthcheck", "", "检查指定健康地址后退出")
	showVersion := flag.Bool("version", false, "显示程序版本后退出")
	applyUpdate := flag.String("apply-update", "", "应用已下载的网关更新")
	flag.Parse()
	if *showVersion {
		fmt.Println("zcode2api " + version)
		return
	}
	if *applyUpdate != "" {
		if err := server.ApplyUpdate(*applyUpdate); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *health != "" {
		if *health == "auto" {
			data := os.Getenv("ZCODE2API_DATA_DIR")
			if data == "" {
				data = filepath.Join(*root, "data")
			}
			b, e := os.ReadFile(filepath.Join(data, "ports.json"))
			var info struct {
				Port int `json:"gateway_port"`
			}
			if e != nil || json.Unmarshal(b, &info) != nil || info.Port <= 0 {
				os.Exit(1)
			}
			*health = fmt.Sprintf("http://127.0.0.1:%d/healthz", info.Port)
		}
		c := http.Client{Timeout: 4 * time.Second}
		r, e := c.Get(*health)
		if e != nil {
			os.Exit(1)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	abs, e := filepath.Abs(*root)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	data := os.Getenv("ZCODE2API_DATA_DIR")
	if data == "" {
		data = filepath.Join(abs, "data")
	}
	web, _ := fs.Sub(assets, "src/web")
	app, e := server.New(abs, data, web, version)
	if e != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", e)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer app.Close()
	if e = app.Run(ctx); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
