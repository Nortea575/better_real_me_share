package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"
)

const serviceVersion = "1.0.0"

type Config struct {
	DataDir   string
	Listen    string
	PublicURL string
	BasePath  string
	TLSCert   string
	TLSKey    string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "setup-link":
		return runSetupLink(args[1:])
	case "check-listen":
		return runCheckListen(args[1:])
	case "version", "--version", "-version":
		fmt.Printf("real-me-share %s\n", serviceVersion)
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		return usageError()
	}
}

func usageError() error {
	printUsage()
	return fmt.Errorf("必须指定 serve 或 setup-link 子命令")
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "用法:")
	fmt.Fprintln(os.Stderr, "  brm-share serve --data-dir DIR --listen HOST:PORT --public-url URL [--tls-cert FILE --tls-key FILE]")
	fmt.Fprintln(os.Stderr, "  brm-share setup-link --data-dir DIR --public-url URL")
	fmt.Fprintln(os.Stderr, "  brm-share check-listen --listen HOST:PORT")
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dataDir := fs.String("data-dir", envOr("REAL_ME_DATA_DIR", ""), "持久化数据目录")
	listen := fs.String("listen", envOr("REAL_ME_LISTEN", "127.0.0.1:18766"), "监听地址")
	publicURL := fs.String("public-url", envOr("REAL_ME_PUBLIC_URL", ""), "用户访问的公开 URL（可带路径前缀）")
	tlsCert := fs.String("tls-cert", os.Getenv("REAL_ME_TLS_CERT"), "HTTPS 证书文件")
	tlsKey := fs.String("tls-key", os.Getenv("REAL_ME_TLS_KEY"), "HTTPS 私钥文件")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("serve 不接受多余参数")
	}
	cfg, err := normalizeConfig(Config{DataDir: *dataDir, Listen: *listen, PublicURL: *publicURL, TLSCert: *tlsCert, TLSKey: *tlsKey})
	if err != nil {
		return err
	}
	store, err := openStore(cfg.DataDir)
	if err != nil {
		return err
	}
	defer store.close()
	app := newApp(cfg, store, webAssetFS())
	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           app,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() {
		if cfg.TLSCert != "" {
			serveErr <- server.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			serveErr <- server.ListenAndServe()
		}
	}()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("HTTP 服务启动失败: %w", err)
	case <-ctx.Done():
		app.closeStreams()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("服务关闭失败: %w", err)
		}
		err := <-serveErr
		if !errors.Is(err, http.ErrServerClosed) && err != nil {
			return fmt.Errorf("HTTP 服务退出失败: %w", err)
		}
		return nil
	}
}

func runSetupLink(args []string) error {
	fs := flag.NewFlagSet("setup-link", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dataDir := fs.String("data-dir", envOr("REAL_ME_DATA_DIR", ""), "持久化数据目录")
	publicURL := fs.String("public-url", envOr("REAL_ME_PUBLIC_URL", ""), "用户访问的公开 URL（可带路径前缀）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("setup-link 不接受多余参数")
	}
	cfg, err := normalizeConfig(Config{DataDir: *dataDir, PublicURL: *publicURL})
	if err != nil {
		return err
	}
	store, err := openStore(cfg.DataDir)
	if err != nil {
		return err
	}
	defer store.close()
	secret, err := createSetupToken(context.Background(), store)
	if err != nil {
		return err
	}
	fmt.Printf("初始管理链接（仅显示一次，请勿公开）：%s#setup=%s\n", cfg.PublicURL, secret)
	return nil
}

func runCheckListen(args []string) error {
	fs := flag.NewFlagSet("check-listen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	listen := fs.String("listen", envOr("REAL_ME_LISTEN", "0.0.0.0:18766"), "待检查的监听地址")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("check-listen 不接受多余参数")
	}
	return checkListen(*listen)
}

func checkListen(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("监听地址当前无法绑定；请检查地址或端口占用")
	}
	return listener.Close()
}

func createSetupToken(ctx context.Context, store *Store) (string, error) {
	secret, err := randomSecret(32)
	if err != nil {
		return "", fmt.Errorf("无法生成 setup token")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("无法准备 setup token")
	}
	defer tx.Rollback()
	var admin int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM admin WHERE singleton=1`).Scan(&admin)
	if err == nil {
		return "", fmt.Errorf("管理员已初始化；不能重新生成 setup 链接")
	}
	if err != sql.ErrNoRows {
		return "", fmt.Errorf("无法检查管理员状态")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO setup_tokens(singleton,token_hash,created_at) VALUES (1,?,?)
ON CONFLICT(singleton) DO UPDATE SET token_hash=excluded.token_hash,created_at=excluded.created_at`, secretHash(secret), nowRFC3339()); err != nil {
		return "", fmt.Errorf("无法保存 setup token")
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("无法保存 setup token")
	}
	return secret, nil
}

func normalizeConfig(cfg Config) (Config, error) {
	if cfg.DataDir == "" {
		return cfg, fmt.Errorf("必须指定 --data-dir 或 REAL_ME_DATA_DIR")
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:18766"
	}
	if cfg.Listen != "" {
		if _, _, err := netSplitHostPort(cfg.Listen); err != nil {
			return cfg, fmt.Errorf("监听地址必须是 HOST:PORT")
		}
	}
	if cfg.PublicURL == "" {
		return cfg, fmt.Errorf("必须指定 --public-url 或 REAL_ME_PUBLIC_URL")
	}
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return cfg, fmt.Errorf("公开 URL 必须是无凭据、查询和片段的 http(s) URL")
	}
	if strings.ContainsAny(cfg.PublicURL, "\r\n\x00\\") || strings.Contains(u.Host, "%") {
		return cfg, fmt.Errorf("公开 URL 包含不支持的字符")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return cfg, fmt.Errorf("公开 URL 路径不能包含 . 或 .. 段")
		}
	}
	clean := path.Clean("/" + strings.TrimPrefix(u.Path, "/"))
	if clean == "." {
		clean = "/"
	}
	if !strings.HasSuffix(clean, "/") {
		clean += "/"
	}
	u.Path = clean
	u.RawPath = ""
	cfg.PublicURL = u.String()
	cfg.BasePath = clean
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return cfg, fmt.Errorf("--tls-cert 与 --tls-key 必须同时设置")
	}
	if cfg.TLSCert != "" && u.Scheme != "https" {
		return cfg, fmt.Errorf("启用本机 TLS 时公开 URL 必须使用 https")
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func netSplitHostPort(value string) (string, string, error) {
	return net.SplitHostPort(value)
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }
