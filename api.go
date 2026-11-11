package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

const (
	adminSessionLifetime = 12 * time.Hour
	bindingLifetime      = 20 * time.Minute
	staleAfter           = 5 * time.Minute
)

type App struct {
	cfg      Config
	store    *Store
	web      fs.FS
	streamMu sync.Mutex
	streams  map[string]*publisherStream
}

type APIError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

type Connection struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Paired           bool    `json:"paired"`
	PublicEnabled    bool    `json:"public_enabled"`
	ViewURL          string  `json:"view_url"`
	CreatedAt        string  `json:"created_at"`
	LastReceivedAt   *string `json:"last_received_at"`
	BindingExpiresAt *string `json:"binding_expires_at"`
}

type connectionRow struct {
	id               string
	name             string
	viewCode         string
	paired           bool
	publicEnabled    bool
	createdAt        string
	lastReceivedAt   *string
	bindingExpiresAt *string
}

func newApp(cfg Config, store *Store, web fs.FS) *App {
	return &App{cfg: cfg, store: store, web: web, streams: make(map[string]*publisherStream)}
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; style-src-attr 'unsafe-inline'; img-src 'self' https: data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
	if a.cfg.BasePath != "/" {
		withoutTrailing := strings.TrimSuffix(a.cfg.BasePath, "/")
		if r.URL.Path == withoutTrailing {
			http.Redirect(w, r, a.cfg.BasePath, http.StatusPermanentRedirect)
			return
		}
		if !strings.HasPrefix(r.URL.Path, a.cfg.BasePath) {
			http.NotFound(w, r)
			return
		}
	}
	rel := strings.TrimPrefix(r.URL.Path, a.cfg.BasePath)
	if a.cfg.BasePath == "/" {
		rel = strings.TrimPrefix(r.URL.Path, "/")
	}
	if strings.HasPrefix(rel, "api/") || rel == "api" {
		a.serveAPI(w, r, rel)
		return
	}
	a.serveWeb(w, r, rel)
}

func (a *App) serveAPI(w http.ResponseWriter, r *http.Request, rel string) {
	parts := strings.Split(rel, "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "v1" {
		a.writeError(w, http.StatusNotFound, "未找到此接口", "not_found")
		return
	}
	if len(parts) == 3 && parts[2] == "info" {
		if r.Method != http.MethodGet {
			a.methodNotAllowed(w)
			return
		}
		needsSetup, err := a.store.hasAdmin(r.Context())
		if err != nil {
			a.internalError(w)
			return
		}
		a.writeJSON(w, http.StatusOK, map[string]any{
			"service_version": serviceVersion,
			"protocol":        1,
			"refresh_mode":    "on_request",
			"needs_setup":     !needsSetup,
			"public_url":      a.cfg.PublicURL,
			"base_path":       a.cfg.BasePath,
		})
		return
	}
	if len(parts) == 3 && parts[2] == "setup" {
		a.setup(w, r)
		return
	}
	if len(parts) == 3 && parts[2] == "login" {
		a.login(w, r)
		return
	}
	if len(parts) == 4 && parts[2] == "public" {
		a.publicSnapshot(w, r, parts[3])
		return
	}
	if len(parts) == 3 && parts[2] == "bind" {
		a.bind(w, r)
		return
	}
	if len(parts) == 4 && parts[2] == "publisher" && parts[3] == "status" {
		a.publisherStatus(w, r)
		return
	}
	if len(parts) == 4 && parts[2] == "publisher" && parts[3] == "events" {
		a.publisherEvents(w, r)
		return
	}
	if len(parts) == 4 && parts[2] == "publisher" && parts[3] == "snapshot" {
		a.publisherSnapshot(w, r)
		return
	}
	if len(parts) == 4 && parts[2] == "publisher" && parts[3] == "visibility" {
		a.publisherVisibility(w, r)
		return
	}
	if len(parts) == 3 && parts[2] == "logout" {
		a.logout(w, r)
		return
	}
	if len(parts) == 3 && parts[2] == "connections" {
		a.connections(w, r)
		return
	}
	if len(parts) >= 4 && parts[2] == "connections" {
		a.connectionRoute(w, r, parts[3:])
		return
	}
	a.writeError(w, http.StatusNotFound, "未找到此接口", "not_found")
}

func (a *App) setup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.methodNotAllowed(w)
		return
	}
	var req struct {
		SetupToken string `json:"setup_token"`
		Password   string `json:"password"`
	}
	if err := decodeJSONBody(w, r, 16<<10, &req); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	if len(req.SetupToken) < 32 || len(req.SetupToken) > 256 || len(req.Password) < 12 || len(req.Password) > 1024 {
		a.writeError(w, http.StatusBadRequest, "setup token 或密码长度无效", "invalid_credentials")
		return
	}
	session, expires, err := a.initializeAdmin(r.Context(), req.SetupToken, req.Password)
	if err != nil {
		if err == errSetupAlreadyDone {
			a.writeError(w, http.StatusConflict, "管理员已完成初始化", "already_setup")
		} else if err == errBadSetupToken {
			a.writeError(w, http.StatusUnauthorized, "setup 链接无效或已使用", "invalid_setup_token")
		} else {
			a.internalError(w)
		}
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"token": session, "expires_at": expires})
}

var (
	errSetupAlreadyDone = fmt.Errorf("setup already done")
	errBadSetupToken    = fmt.Errorf("invalid setup token")
)

func (a *App) initializeAdmin(ctx context.Context, setupToken, password string) (string, string, error) {
	var salt [32]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", "", err
	}
	passwordHash := derivePassword(password, salt[:], passwordIterations)
	token, err := randomSecret(32)
	if err != nil {
		return "", "", err
	}
	expires := time.Now().UTC().Add(adminSessionLifetime).Format(time.RFC3339Nano)
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var existing int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM admin WHERE singleton=1`).Scan(&existing)
	if err == nil {
		return "", "", errSetupAlreadyDone
	}
	if err != sql.ErrNoRows {
		return "", "", err
	}
	var storedHash string
	err = tx.QueryRowContext(ctx, `SELECT token_hash FROM setup_tokens WHERE singleton=1`).Scan(&storedHash)
	if err != nil || subtle.ConstantTimeCompare([]byte(storedHash), []byte(secretHash(setupToken))) != 1 {
		return "", "", errBadSetupToken
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin(singleton,password_salt,password_hash,iterations,created_at) VALUES(1,?,?,?,?)`, salt[:], passwordHash, passwordIterations, nowRFC3339()); err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM setup_tokens`); err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_sessions(token_hash,expires_at) VALUES(?,?)`, secretHash(token), expires); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return token, expires, nil
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.methodNotAllowed(w)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSONBody(w, r, 16<<10, &req); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	var salt, expected []byte
	var iterations int
	err := a.store.db.QueryRowContext(r.Context(), `SELECT password_salt,password_hash,iterations FROM admin WHERE singleton=1`).Scan(&salt, &expected, &iterations)
	if err == sql.ErrNoRows {
		a.writeError(w, http.StatusConflict, "请先使用 setup 链接初始化管理员", "setup_required")
		return
	}
	if err != nil {
		a.internalError(w)
		return
	}
	if !passwordMatches(req.Password, salt, expected, iterations) {
		a.writeError(w, http.StatusUnauthorized, "密码错误", "invalid_credentials")
		return
	}
	token, err := randomSecret(32)
	if err != nil {
		a.internalError(w)
		return
	}
	expires := time.Now().UTC().Add(adminSessionLifetime).Format(time.RFC3339Nano)
	if _, err := a.store.db.ExecContext(r.Context(), `INSERT INTO admin_sessions(token_hash,expires_at) VALUES(?,?)`, secretHash(token), expires); err != nil {
		a.internalError(w)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_at": expires})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.methodNotAllowed(w)
		return
	}
	token, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	if _, err := a.store.db.ExecContext(r.Context(), `DELETE FROM admin_sessions WHERE token_hash=?`, secretHash(token)); err != nil {
		a.internalError(w)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) connections(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		a.methodNotAllowed(w)
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSONBody(w, r, 16<<10, &req); err != nil {
			a.writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if !validText(req.Name, 1, 80) {
			a.writeError(w, http.StatusBadRequest, "名称长度必须为 1 到 80 个字符", "invalid_name")
			return
		}
		conn, bindingURL, expires, err := a.createConnection(r.Context(), req.Name)
		if err != nil {
			a.internalError(w)
			return
		}
		a.writeJSON(w, http.StatusCreated, map[string]any{"connection": conn, "binding_url": bindingURL, "expires_at": expires})
		return
	}
	rows, err := a.store.db.QueryContext(r.Context(), `SELECT c.id,c.name,c.view_code,c.paired,c.public_enabled,c.created_at,s.received_at,c.binding_expires_at FROM connections c LEFT JOIN snapshots s ON s.connection_id=c.id ORDER BY c.created_at,c.id`)
	if err != nil {
		a.internalError(w)
		return
	}
	defer rows.Close()
	connections := make([]Connection, 0)
	for rows.Next() {
		row, err := scanConnection(rows)
		if err != nil {
			a.internalError(w)
			return
		}
		connections = append(connections, a.publicConnection(row))
	}
	if err := rows.Err(); err != nil {
		a.internalError(w)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"connections": connections})
}

func (a *App) createConnection(ctx context.Context, name string) (Connection, string, string, error) {
	id, err := randomID(16)
	if err != nil {
		return Connection{}, "", "", err
	}
	viewCode, err := randomSecret(24)
	if err != nil {
		return Connection{}, "", "", err
	}
	ticket, err := randomSecret(32)
	if err != nil {
		return Connection{}, "", "", err
	}
	now := time.Now().UTC()
	created := now.Format(time.RFC3339Nano)
	expires := now.Add(bindingLifetime).Format(time.RFC3339Nano)
	if _, err := a.store.db.ExecContext(ctx, `INSERT INTO connections(id,name,view_code,paired,public_enabled,created_at,binding_hash,binding_expires_at) VALUES(?,?,?,0,0,?,?,?)`, id, name, viewCode, created, secretHash(ticket), expires); err != nil {
		return Connection{}, "", "", err
	}
	row := connectionRow{id: id, name: name, viewCode: viewCode, createdAt: created, bindingExpiresAt: &expires}
	return a.publicConnection(row), a.cfg.PublicURL + "#bind=" + ticket, expires, nil
}

func (a *App) connectionRoute(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 0 || parts[0] == "" {
		a.writeError(w, http.StatusNotFound, "未找到此接口", "not_found")
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodDelete {
			a.methodNotAllowed(w)
			return
		}
		a.streamMu.Lock()
		result, err := a.store.db.ExecContext(r.Context(), `DELETE FROM connections WHERE id=?`, id)
		if err != nil {
			a.streamMu.Unlock()
			a.internalError(w)
			return
		}
		count, _ := result.RowsAffected()
		if count > 0 {
			a.closePublisherStreamLocked(id)
		}
		a.streamMu.Unlock()
		if count == 0 {
			a.writeError(w, http.StatusNotFound, "连接不存在", "not_found")
			return
		}
		a.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if len(parts) != 2 {
		a.writeError(w, http.StatusNotFound, "未找到此接口", "not_found")
		return
	}
	switch parts[1] {
	case "binding":
		if r.Method != http.MethodPost {
			a.methodNotAllowed(w)
			return
		}
		conn, bindingURL, expires, err := a.rebindConnection(r.Context(), id)
		if err == sql.ErrNoRows {
			a.writeError(w, http.StatusNotFound, "连接不存在", "not_found")
			return
		}
		if err != nil {
			a.internalError(w)
			return
		}
		a.writeJSON(w, http.StatusOK, map[string]any{"connection": conn, "binding_url": bindingURL, "expires_at": expires})
	case "visibility":
		a.adminVisibility(w, r, id)
	case "rotate":
		if r.Method != http.MethodPost {
			a.methodNotAllowed(w)
			return
		}
		conn, err := a.rotateViewer(r.Context(), id)
		if err == sql.ErrNoRows {
			a.writeError(w, http.StatusNotFound, "连接不存在", "not_found")
			return
		}
		if err != nil {
			a.internalError(w)
			return
		}
		a.writeJSON(w, http.StatusOK, conn)
	default:
		a.writeError(w, http.StatusNotFound, "未找到此接口", "not_found")
	}
}

func (a *App) rebindConnection(ctx context.Context, id string) (Connection, string, string, error) {
	ticket, err := randomSecret(32)
	if err != nil {
		return Connection{}, "", "", err
	}
	now := time.Now().UTC()
	expires := now.Add(bindingLifetime).Format(time.RFC3339Nano)
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Connection{}, "", "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE connections SET binding_hash=?,binding_expires_at=?,paired=0,public_enabled=0,last_sequence=0 WHERE id=?`, secretHash(ticket), expires, id); err != nil {
		return Connection{}, "", "", err
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM connections WHERE id=?`, id).Scan(&exists)
	if err != nil {
		return Connection{}, "", "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM publishers WHERE connection_id=?`, id); err != nil {
		return Connection{}, "", "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE connection_id=?`, id); err != nil {
		return Connection{}, "", "", err
	}
	if err := tx.Commit(); err != nil {
		return Connection{}, "", "", err
	}
	a.closePublisherStreamLocked(id)
	row, err := a.getConnection(ctx, id)
	if err != nil {
		return Connection{}, "", "", err
	}
	return a.publicConnection(row), a.cfg.PublicURL + "#bind=" + ticket, expires, nil
}

func (a *App) adminVisibility(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		a.methodNotAllowed(w)
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSONBody(w, r, 16<<10, &req); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	result, err := a.store.db.ExecContext(r.Context(), `UPDATE connections SET public_enabled=? WHERE id=?`, boolInt(req.Enabled), id)
	if err != nil {
		a.internalError(w)
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		a.writeError(w, http.StatusNotFound, "连接不存在", "not_found")
		return
	}
	if !req.Enabled {
		a.notifyVisibilityClosedLocked(id)
	}
	row, err := a.getConnection(r.Context(), id)
	if err != nil {
		a.internalError(w)
		return
	}
	a.writeJSON(w, http.StatusOK, a.publicConnection(row))
}

func (a *App) rotateViewer(ctx context.Context, id string) (Connection, error) {
	viewCode, err := randomSecret(24)
	if err != nil {
		return Connection{}, err
	}
	result, err := a.store.db.ExecContext(ctx, `UPDATE connections SET view_code=? WHERE id=?`, viewCode, id)
	if err != nil {
		return Connection{}, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return Connection{}, sql.ErrNoRows
	}
	row, err := a.getConnection(ctx, id)
	if err != nil {
		return Connection{}, err
	}
	return a.publicConnection(row), nil
}

func (a *App) bind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.methodNotAllowed(w)
		return
	}
	var req struct {
		Ticket       string `json:"ticket"`
		AgentVersion string `json:"agent_version"`
	}
	if err := decodeJSONBody(w, r, 16<<10, &req); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	if len(req.Ticket) < 32 || len(req.Ticket) > 256 || !validText(req.AgentVersion, 1, 80) {
		a.writeError(w, http.StatusBadRequest, "绑定信息无效", "invalid_binding")
		return
	}
	publisher, err := randomSecret(32)
	if err != nil {
		a.internalError(w)
		return
	}
	ctx := r.Context()
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		a.internalError(w)
		return
	}
	defer tx.Rollback()
	var connectionID string
	err = tx.QueryRowContext(ctx, `UPDATE connections SET binding_hash=NULL,binding_expires_at=NULL,paired=1,public_enabled=0 WHERE binding_hash=? AND binding_expires_at>? RETURNING id`, secretHash(req.Ticket), nowRFC3339()).Scan(&connectionID)
	if err != nil {
		if err == sql.ErrNoRows {
			a.writeError(w, http.StatusGone, "绑定链接无效、已过期或已使用", "invalid_ticket")
			return
		}
		a.internalError(w)
		return
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM publishers WHERE connection_id=?`, connectionID); err != nil {
		a.internalError(w)
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO publishers(connection_id,token_hash,created_at) VALUES(?,?,?)`, connectionID, secretHash(publisher), nowRFC3339()); err != nil {
		a.internalError(w)
		return
	}
	if err := tx.Commit(); err != nil {
		a.internalError(w)
		return
	}
	conn, err := a.getConnection(ctx, connectionID)
	if err != nil {
		a.internalError(w)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"protocol":        1,
		"connection_id":   connectionID,
		"publisher_token": publisher,
		"view_url":        a.viewerURL(conn.viewCode),
		"public_enabled":  false,
	})
}

func (a *App) publisherStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		a.methodNotAllowed(w)
		return
	}
	publisher, ok := a.requirePublisher(w, r)
	if !ok {
		return
	}
	conn, err := a.getConnection(r.Context(), publisher.connectionID)
	if err != nil {
		a.internalError(w)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"connection_id":    conn.id,
		"view_url":         a.viewerURL(conn.viewCode),
		"public_enabled":   conn.publicEnabled,
		"last_received_at": conn.lastReceivedAt,
	})
}

func (a *App) publisherSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.methodNotAllowed(w)
		return
	}
	publisher, ok := a.requirePublisher(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSnapshotBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.writeError(w, http.StatusRequestEntityTooLarge, "快照超过 2 MiB 限制", "payload_too_large")
		return
	}
	snapshot, canonical, err := decodeSnapshot(body)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error(), "invalid_snapshot")
		return
	}
	ctx := r.Context()
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		a.internalError(w)
		return
	}
	defer tx.Rollback()
	active, err := publisherStillBound(ctx, tx, publisher)
	if err != nil {
		a.internalError(w)
		return
	}
	if !active {
		a.writeError(w, http.StatusUnauthorized, "发布者凭证已失效", "unauthorized")
		return
	}
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT last_sequence FROM connections WHERE id=?`, publisher.connectionID).Scan(&current)
	if err != nil {
		a.internalError(w)
		return
	}
	accepted := true
	received := nowRFC3339()
	if snapshot.Sequence <= current {
		accepted = false
		var existingReceived sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT received_at FROM snapshots WHERE connection_id=?`, publisher.connectionID).Scan(&existingReceived); err != nil && err != sql.ErrNoRows {
			a.internalError(w)
			return
		}
		if existingReceived.Valid {
			received = existingReceived.String
		}
	} else {
		if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots(connection_id,sequence,generated_at,source_updated_at,received_at,json) VALUES(?,?,?,?,?,?)
ON CONFLICT(connection_id) DO UPDATE SET sequence=excluded.sequence,generated_at=excluded.generated_at,source_updated_at=excluded.source_updated_at,received_at=excluded.received_at,json=excluded.json`, publisher.connectionID, snapshot.Sequence, snapshot.GeneratedAt, nullableString(snapshot.SourceUpdatedAt), received, string(canonical)); err != nil {
			a.internalError(w)
			return
		}
		if _, err := tx.ExecContext(ctx, `UPDATE connections SET last_sequence=? WHERE id=?`, snapshot.Sequence, publisher.connectionID); err != nil {
			a.internalError(w)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		a.internalError(w)
		return
	}
	if accepted {
		a.completePendingRefresh(publisher.connectionID)
	}
	conn, err := a.getConnection(ctx, publisher.connectionID)
	if err != nil {
		a.internalError(w)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"accepted":       accepted,
		"received_at":    received,
		"view_url":       a.viewerURL(conn.viewCode),
		"public_enabled": conn.publicEnabled,
	})
}

func (a *App) publisherVisibility(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.methodNotAllowed(w)
		return
	}
	publisher, ok := a.requirePublisher(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSONBody(w, r, 16<<10, &req); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		a.internalError(w)
		return
	}
	defer tx.Rollback()
	active, err := publisherStillBound(r.Context(), tx, publisher)
	if err != nil {
		a.internalError(w)
		return
	}
	if !active {
		a.writeError(w, http.StatusUnauthorized, "发布者凭证已失效", "unauthorized")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE connections SET public_enabled=? WHERE id=?`, boolInt(req.Enabled), publisher.connectionID); err != nil {
		a.internalError(w)
		return
	}
	if err := tx.Commit(); err != nil {
		a.internalError(w)
		return
	}
	if !req.Enabled {
		a.notifyVisibilityClosedLocked(publisher.connectionID)
	}
	conn, err := a.getConnection(r.Context(), publisher.connectionID)
	if err != nil {
		a.internalError(w)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"view_url": a.viewerURL(conn.viewCode), "public_enabled": conn.publicEnabled})
}

func (a *App) publicSnapshot(w http.ResponseWriter, r *http.Request, viewCode string) {
	if r.Method != http.MethodGet {
		a.methodNotAllowed(w)
		return
	}
	a.streamMu.Lock()
	data, found, err := a.readPublicSnapshot(r.Context(), viewCode)
	if err != nil {
		a.streamMu.Unlock()
		a.internalError(w)
		return
	}
	if !found {
		a.streamMu.Unlock()
		a.writeError(w, http.StatusNotFound, "分享链接无效或当前未公开", "not_found")
		return
	}
	pending := a.requestRefreshLocked(data.connectionID)
	stream := a.streams[data.connectionID]
	a.streamMu.Unlock()
	refreshed := false
	if pending != nil {
		wait := time.NewTimer(publicRefreshWait)
		defer wait.Stop()
		select {
		case <-pending.done:
		case <-wait.C:
			a.expirePendingRefresh(data.connectionID, stream, pending)
		case <-r.Context().Done():
			a.expirePendingRefresh(data.connectionID, stream, pending)
			return
		}
		refreshed = a.pendingRefreshReceived(pending)
		data, found, err = a.readPublicSnapshot(r.Context(), viewCode)
		if err != nil {
			a.internalError(w)
			return
		}
		if !found {
			a.writeError(w, http.StatusNotFound, "分享链接无效或当前未公开", "not_found")
			return
		}
	}
	a.writeJSON(w, http.StatusOK, data.response(refreshed))
}

type publicSnapshotData struct {
	connectionID string
	name         string
	snapshot     json.RawMessage
	receivedAt   *string
	stale        bool
}

func (a *App) readPublicSnapshot(ctx context.Context, viewCode string) (publicSnapshotData, bool, error) {
	var name string
	var connectionID string
	var raw, received sql.NullString
	err := a.store.db.QueryRowContext(ctx, `SELECT c.id,c.name,s.json,s.received_at FROM connections c LEFT JOIN snapshots s ON s.connection_id=c.id WHERE c.view_code=? AND c.public_enabled=1`, viewCode).Scan(&connectionID, &name, &raw, &received)
	if err == sql.ErrNoRows {
		return publicSnapshotData{}, false, nil
	}
	if err != nil {
		return publicSnapshotData{}, false, err
	}
	data := publicSnapshotData{connectionID: connectionID, name: name}
	if raw.Valid {
		data.snapshot = json.RawMessage(raw.String)
	}
	if received.Valid {
		value := received.String
		data.receivedAt = &value
		if at, parseErr := time.Parse(time.RFC3339Nano, value); parseErr == nil {
			data.stale = time.Since(at) > staleAfter
		}
	}
	return data, true, nil
}

func (data publicSnapshotData) response(refreshed bool) map[string]any {
	return map[string]any{
		"snapshot":    data.snapshot,
		"received_at": data.receivedAt,
		"stale":       data.stale,
		"refreshed":   refreshed,
		"connection":  map[string]string{"name": data.name},
	}
}

type publisherIdentity struct {
	connectionID string
	tokenHash    string
}

func (a *App) requireAdmin(w http.ResponseWriter, r *http.Request) (string, bool) {
	token, ok := bearerToken(r)
	if !ok {
		a.writeError(w, http.StatusUnauthorized, "需要管理员登录", "unauthorized")
		return "", false
	}
	var valid int
	err := a.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM admin_sessions WHERE token_hash=? AND expires_at>?`, secretHash(token), nowRFC3339()).Scan(&valid)
	if err != nil {
		a.writeError(w, http.StatusUnauthorized, "管理员会话无效或已过期", "unauthorized")
		return "", false
	}
	return token, true
}

func (a *App) requirePublisher(w http.ResponseWriter, r *http.Request) (publisherIdentity, bool) {
	token, ok := bearerToken(r)
	if !ok {
		a.writeError(w, http.StatusUnauthorized, "需要发布者凭证", "unauthorized")
		return publisherIdentity{}, false
	}
	identity := publisherIdentity{tokenHash: secretHash(token)}
	err := a.store.db.QueryRowContext(r.Context(), `SELECT connection_id FROM publishers WHERE token_hash=?`, identity.tokenHash).Scan(&identity.connectionID)
	if err != nil {
		a.writeError(w, http.StatusUnauthorized, "发布者凭证无效", "unauthorized")
		return publisherIdentity{}, false
	}
	return identity, true
}

func publisherStillBound(ctx context.Context, tx *sql.Tx, publisher publisherIdentity) (bool, error) {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM publishers WHERE connection_id=? AND token_hash=?`, publisher.connectionID, publisher.tokenHash).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func bearerToken(r *http.Request) (string, bool) {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(value, "Bearer ")
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

func (a *App) getConnection(ctx context.Context, id string) (connectionRow, error) {
	row := a.store.db.QueryRowContext(ctx, `SELECT c.id,c.name,c.view_code,c.paired,c.public_enabled,c.created_at,s.received_at,c.binding_expires_at FROM connections c LEFT JOIN snapshots s ON s.connection_id=c.id WHERE c.id=?`, id)
	return scanConnection(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanConnection(row scanner) (connectionRow, error) {
	var conn connectionRow
	var paired, public int
	var received, binding sql.NullString
	err := row.Scan(&conn.id, &conn.name, &conn.viewCode, &paired, &public, &conn.createdAt, &received, &binding)
	if err != nil {
		return connectionRow{}, err
	}
	conn.paired = paired != 0
	conn.publicEnabled = public != 0
	if received.Valid {
		conn.lastReceivedAt = &received.String
	}
	if binding.Valid {
		conn.bindingExpiresAt = &binding.String
	}
	return conn, nil
}

func (a *App) publicConnection(row connectionRow) Connection {
	return Connection{
		ID: row.id, Name: row.name, Paired: row.paired, PublicEnabled: row.publicEnabled,
		ViewURL: a.viewerURL(row.viewCode), CreatedAt: row.createdAt,
		LastReceivedAt: row.lastReceivedAt, BindingExpiresAt: row.bindingExpiresAt,
	}
}

func (a *App) viewerURL(code string) string { return a.cfg.PublicURL + "#/view/" + code }

func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		if err == io.EOF {
			return fmt.Errorf("请求体不能为空")
		}
		if strings.Contains(err.Error(), "request body too large") {
			return fmt.Errorf("请求体过大")
		}
		return fmt.Errorf("请求 JSON 格式或字段无效")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("请求必须只包含一个 JSON 对象")
	}
	return nil
}

func (a *App) serveWeb(w http.ResponseWriter, r *http.Request, rel string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		a.methodNotAllowed(w)
		return
	}
	assetPath := "index.html"
	if rel != "" {
		clean := path.Clean(rel)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(rel, "/") {
			http.NotFound(w, r)
			return
		}
		if clean == "assets" || strings.HasPrefix(clean, "assets/") {
			assetPath = clean
		} else {
			http.NotFound(w, r)
			return
		}
	}
	data, err := fs.ReadFile(a.web, assetPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(assetPath, "assets/") {
		etag := fmt.Sprintf("\"%x\"", sha256.Sum256(data))
		w.Header().Set("Cache-Control", "public, no-cache")
		w.Header().Set("ETag", etag)
		if etagMatches(strings.Join(r.Header.Values("If-None-Match"), ","), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	contentType := mime.TypeByExtension(path.Ext(assetPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func etagMatches(ifNoneMatch, etag string) bool {
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

func (a *App) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (a *App) writeError(w http.ResponseWriter, status int, message, code string) {
	a.writeJSON(w, status, APIError{Error: message, Code: code})
}

func (a *App) methodNotAllowed(w http.ResponseWriter) {
	a.writeError(w, http.StatusMethodNotAllowed, "此请求方法不受支持", "method_not_allowed")
}

func (a *App) internalError(w http.ResponseWriter) {
	a.writeError(w, http.StatusInternalServerError, "服务暂时无法处理请求", "internal_error")
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
