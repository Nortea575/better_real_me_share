package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"testing/fstest"
)

type testEnv struct {
	app   *App
	store *Store
	cfg   Config
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfg, err := normalizeConfig(Config{DataDir: dir, Listen: "127.0.0.1:18766", PublicURL: "http://share.example.test/test-life/"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })
	web := fstest.MapFS{
		"index.html":       {Data: []byte("index asset")},
		"assets/app.js":    {Data: []byte("app asset")},
		"assets/style.css": {Data: []byte("style asset")},
	}
	return &testEnv{app: newApp(cfg, store, web), store: store, cfg: cfg}
}

func (e *testEnv) request(t *testing.T, method, route string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		switch v := body.(type) {
		case json.RawMessage:
			payload = v
		case []byte:
			payload = v
		default:
			var err error
			payload, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	req := httptest.NewRequest(method, route, bytes.NewReader(payload))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	e.app.ServeHTTP(response, req)
	return response
}

func (e *testEnv) route(suffix string) string {
	return strings.TrimSuffix(e.cfg.BasePath, "/") + suffix
}

func (e *testEnv) setupAdmin(t *testing.T) string {
	t.Helper()
	secret, err := createSetupToken(context.Background(), e.store)
	if err != nil {
		t.Fatal(err)
	}
	bad := e.request(t, http.MethodPost, e.route("/api/v1/setup"), map[string]string{"setup_token": secret + "x", "password": "correct-horse-battery"}, "")
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("invalid setup token status = %d, want 401: %s", bad.Code, bad.Body.String())
	}
	response := e.request(t, http.MethodPost, e.route("/api/v1/setup"), map[string]string{"setup_token": secret, "password": "correct-horse-battery"}, "")
	if response.Code != http.StatusOK {
		t.Fatalf("setup status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var result struct {
		Token string `json:"token"`
	}
	decodeResponse(t, response, &result)
	return result.Token
}

func (e *testEnv) createConnection(t *testing.T, admin, name string) (Connection, string) {
	t.Helper()
	response := e.request(t, http.MethodPost, e.route("/api/v1/connections"), map[string]string{"name": name}, admin)
	if response.Code != http.StatusCreated {
		t.Fatalf("create connection status = %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Connection Connection `json:"connection"`
		BindingURL string     `json:"binding_url"`
	}
	decodeResponse(t, response, &result)
	return result.Connection, ticketFromLink(t, result.BindingURL, "#bind=")
}

func (e *testEnv) bind(t *testing.T, ticket string) (string, string) {
	t.Helper()
	response := e.request(t, http.MethodPost, e.route("/api/v1/bind"), map[string]string{"ticket": ticket, "agent_version": "1.0.0"}, "")
	if response.Code != http.StatusOK {
		t.Fatalf("bind status = %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		ConnectionID   string `json:"connection_id"`
		PublisherToken string `json:"publisher_token"`
		ViewURL        string `json:"view_url"`
	}
	decodeResponse(t, response, &result)
	return result.ConnectionID, result.PublisherToken
}

func (e *testEnv) createBound(t *testing.T, admin, name string) (Connection, string) {
	t.Helper()
	conn, ticket := e.createConnection(t, admin, name)
	e.bind(t, ticket)
	return conn, ticket
}

func (e *testEnv) setPublisherVisibility(t *testing.T, token string, enabled bool) *httptest.ResponseRecorder {
	t.Helper()
	return e.request(t, http.MethodPost, e.route("/api/v1/publisher/visibility"), map[string]bool{"enabled": enabled}, token)
}

func (e *testEnv) postSnapshot(t *testing.T, token string, snapshot Snapshot) *httptest.ResponseRecorder {
	t.Helper()
	return e.request(t, http.MethodPost, e.route("/api/v1/publisher/snapshot"), snapshot, token)
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), destination); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
}

func ticketFromLink(t *testing.T, link, marker string) string {
	t.Helper()
	i := strings.LastIndex(link, marker)
	if i < 0 {
		t.Fatalf("link %q missing marker %q", link, marker)
	}
	return link[i+len(marker):]
}

func viewerCode(t *testing.T, link string) string {
	t.Helper()
	return ticketFromLink(t, link, "#/view/")
}

func makeSnapshot(sequence int64, actor string) Snapshot {
	return Snapshot{
		SchemaVersion: 1,
		Sequence:      sequence,
		GeneratedAt:   "2026-11-11T00:00:00Z",
		Actor:         SnapshotActor{Name: actor},
		Life:          SnapshotLife{Enabled: true},
		States:        []SnapshotState{},
		Sections:      SnapshotSections{},
	}
}

func TestSetupLinkIsOneTimeAndCredentialsAreSeparated(t *testing.T) {
	e := newTestEnv(t)
	admin, err := createSetupToken(context.Background(), e.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.db.Exec(`UPDATE setup_tokens SET token_hash=? WHERE singleton=1`, secretHash(admin)); err != nil {
		t.Fatal(err)
	}
	setup := e.request(t, http.MethodPost, e.route("/api/v1/setup"), map[string]string{"setup_token": admin, "password": "correct-horse-battery"}, "")
	if setup.Code != http.StatusOK {
		t.Fatalf("setup failed: %d %s", setup.Code, setup.Body.String())
	}
	var session struct {
		Token string `json:"token"`
	}
	decodeResponse(t, setup, &session)
	if session.Token == "" {
		t.Fatal("setup returned empty admin token")
	}
	reused := e.request(t, http.MethodPost, e.route("/api/v1/setup"), map[string]string{"setup_token": admin, "password": "another-correct-password"}, "")
	if reused.Code != http.StatusConflict && reused.Code != http.StatusUnauthorized {
		t.Fatalf("reused setup token status = %d, want 401 or 409", reused.Code)
	}
	info := e.request(t, http.MethodGet, e.route("/api/v1/info"), nil, "")
	var infoBody map[string]any
	decodeResponse(t, info, &infoBody)
	if infoBody["needs_setup"] != false {
		t.Fatalf("needs_setup = %v, want false", infoBody["needs_setup"])
	}
	if _, err := createSetupToken(context.Background(), e.store); err == nil {
		t.Fatal("setup-link must not issue another setup token after administrator initialization")
	}
	conn, ticket := e.createConnection(t, session.Token, "A")
	_, publisher := e.bind(t, ticket)
	adminOnPublisher := e.request(t, http.MethodGet, e.route("/api/v1/publisher/status"), nil, session.Token)
	if adminOnPublisher.Code != http.StatusUnauthorized {
		t.Fatalf("admin token used as publisher status: %d", adminOnPublisher.Code)
	}
	publisherOnAdmin := e.request(t, http.MethodGet, e.route("/api/v1/connections"), nil, publisher)
	if publisherOnAdmin.Code != http.StatusUnauthorized {
		t.Fatalf("publisher token used as admin: %d", publisherOnAdmin.Code)
	}
	if strings.Contains(adminOnPublisher.Body.String()+publisherOnAdmin.Body.String(), session.Token) || strings.Contains(adminOnPublisher.Body.String()+publisherOnAdmin.Body.String(), publisher) {
		t.Fatal("authorization errors must not echo credentials")
	}
	if conn.ID == "" {
		t.Fatal("create connection omitted id")
	}
}

func TestSetupLinkCommandPrintsPrivateLinkOnce(t *testing.T) {
	dir := t.TempDir()
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = writeEnd
	err = runSetupLink([]string{"--data-dir", dir, "--public-url", "http://localhost:18766/test-life/"})
	_ = writeEnd.Close()
	os.Stdout = previous
	if err != nil {
		_ = readEnd.Close()
		t.Fatal(err)
	}
	output, err := io.ReadAll(readEnd)
	_ = readEnd.Close()
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(output))
	if !strings.Contains(line, "http://localhost:18766/test-life/#setup=") {
		t.Fatalf("setup-link output missing base path and setup fragment: %q", line)
	}
	secret := ticketFromLink(t, line, "#setup=")
	if len(secret) < 32 {
		t.Fatalf("setup token is unexpectedly short: %d", len(secret))
	}
	store, err := openStore(filepath.Clean(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	var stored string
	if err := store.db.QueryRow(`SELECT token_hash FROM setup_tokens WHERE singleton=1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != secretHash(secret) || stored == secret {
		t.Fatal("setup token must be stored only as its hash")
	}
}

func TestCheckListenDetectsOccupiedAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkListen(listener.Addr().String()); err == nil {
		_ = listener.Close()
		t.Fatal("occupied address should not pass installer preflight")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkListen("127.0.0.1:0"); err != nil {
		t.Fatalf("free address should pass installer preflight: %v", err)
	}
}

func TestBindingExpiryReuseRebindRotationAndDelete(t *testing.T) {
	e := newTestEnv(t)
	admin := e.setupAdmin(t)
	conn, ticket := e.createConnection(t, admin, "生活")
	id, publisher := e.bind(t, ticket)
	if id != conn.ID {
		t.Fatalf("bound connection id = %s, want %s", id, conn.ID)
	}
	reused := e.request(t, http.MethodPost, e.route("/api/v1/bind"), map[string]string{"ticket": ticket, "agent_version": "1.0.0"}, "")
	if reused.Code != http.StatusGone {
		t.Fatalf("reused ticket status = %d, want 410", reused.Code)
	}
	expiredConn, expiredTicket := e.createConnection(t, admin, "过期")
	if _, err := e.store.db.Exec(`UPDATE connections SET binding_expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), expiredConn.ID); err != nil {
		t.Fatal(err)
	}
	expired := e.request(t, http.MethodPost, e.route("/api/v1/bind"), map[string]string{"ticket": expiredTicket, "agent_version": "1.0.0"}, "")
	if expired.Code != http.StatusGone {
		t.Fatalf("expired ticket status = %d, want 410", expired.Code)
	}
	if visibility := e.setPublisherVisibility(t, publisher, true); visibility.Code != http.StatusOK {
		t.Fatalf("enable visibility failed: %d %s", visibility.Code, visibility.Body.String())
	}
	oldCode := viewerCode(t, conn.ViewURL)
	visible := e.request(t, http.MethodGet, e.route("/api/v1/public/"+oldCode), nil, "")
	if visible.Code != http.StatusOK {
		t.Fatalf("public view before rotate = %d", visible.Code)
	}
	rotated := e.request(t, http.MethodPost, e.route("/api/v1/connections/"+conn.ID+"/rotate"), map[string]any{}, admin)
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate failed: %d %s", rotated.Code, rotated.Body.String())
	}
	var rotatedConn Connection
	decodeResponse(t, rotated, &rotatedConn)
	oldView := e.request(t, http.MethodGet, e.route("/api/v1/public/"+oldCode), nil, "")
	if oldView.Code != http.StatusNotFound {
		t.Fatalf("old viewer code after rotate = %d, want 404", oldView.Code)
	}
	newView := e.request(t, http.MethodGet, e.route("/api/v1/public/"+viewerCode(t, rotatedConn.ViewURL)), nil, "")
	if newView.Code != http.StatusOK {
		t.Fatalf("new viewer code should remain visible after rotate: %d", newView.Code)
	}
	rebound := e.request(t, http.MethodPost, e.route("/api/v1/connections/"+conn.ID+"/binding"), nil, admin)
	if rebound.Code != http.StatusOK {
		t.Fatalf("rebind failed: %d %s", rebound.Code, rebound.Body.String())
	}
	oldWriter := e.request(t, http.MethodGet, e.route("/api/v1/publisher/status"), nil, publisher)
	if oldWriter.Code != http.StatusUnauthorized {
		t.Fatalf("old publisher after rebind = %d, want 401", oldWriter.Code)
	}
	var reboundBody struct {
		BindingURL string     `json:"binding_url"`
		Connection Connection `json:"connection"`
	}
	decodeResponse(t, rebound, &reboundBody)
	if reboundBody.Connection.PublicEnabled || reboundBody.Connection.Paired {
		t.Fatal("rebind must close visibility and clear paired state")
	}
	reboundCode := viewerCode(t, reboundBody.Connection.ViewURL)
	if response := e.request(t, http.MethodGet, e.route("/api/v1/public/"+reboundCode), nil, ""); response.Code != http.StatusNotFound {
		t.Fatalf("rebound connection public view = %d, want 404", response.Code)
	}
	deleteResponse := e.request(t, http.MethodDelete, e.route("/api/v1/connections/"+conn.ID), nil, admin)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete failed: %d %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	if response := e.request(t, http.MethodGet, e.route("/api/v1/public/"+reboundCode), nil, ""); response.Code != http.StatusNotFound {
		t.Fatalf("deleted connection public view = %d, want 404", response.Code)
	}
	_ = expiredTicket
}

func TestSnapshotIsolationVisibilityAndOldSequence(t *testing.T) {
	e := newTestEnv(t)
	admin := e.setupAdmin(t)
	connA, ticketA := e.createConnection(t, admin, "甲")
	_, pubA := e.bind(t, ticketA)
	connB, ticketB := e.createConnection(t, admin, "乙")
	_, pubB := e.bind(t, ticketB)
	for _, item := range []struct {
		token string
		name  string
		seq   int64
	}{{pubA, "角色甲", 2}, {pubB, "角色乙", 1}} {
		response := e.postSnapshot(t, item.token, makeSnapshot(item.seq, item.name))
		if response.Code != http.StatusOK {
			t.Fatalf("snapshot %s failed: %d %s", item.name, response.Code, response.Body.String())
		}
		if visibility := e.setPublisherVisibility(t, item.token, true); visibility.Code != http.StatusOK {
			t.Fatalf("visibility %s failed: %d %s", item.name, visibility.Code, visibility.Body.String())
		}
	}
	old := e.postSnapshot(t, pubA, makeSnapshot(1, "旧角色甲"))
	if old.Code != http.StatusOK {
		t.Fatalf("old sequence response status = %d", old.Code)
	}
	var oldResult struct {
		Accepted bool `json:"accepted"`
	}
	decodeResponse(t, old, &oldResult)
	if oldResult.Accepted {
		t.Fatal("duplicate or older sequence must be ignored")
	}
	for _, expected := range []struct {
		conn Connection
		name string
		seq  int64
	}{{connA, "角色甲", 2}, {connB, "角色乙", 1}} {
		response := e.request(t, http.MethodGet, e.route("/api/v1/public/"+viewerCode(t, expected.conn.ViewURL)), nil, "")
		if response.Code != http.StatusOK {
			t.Fatalf("public snapshot status = %d: %s", response.Code, response.Body.String())
		}
		var body struct {
			Snapshot Snapshot `json:"snapshot"`
		}
		decodeResponse(t, response, &body)
		if body.Snapshot.Actor.Name != expected.name || body.Snapshot.Sequence != expected.seq {
			t.Fatalf("connection %s returned actor=%q seq=%d", expected.conn.Name, body.Snapshot.Actor.Name, body.Snapshot.Sequence)
		}
	}
	if response := e.request(t, http.MethodGet, e.route("/api/v1/public/not-a-code"), nil, ""); response.Code != http.StatusNotFound {
		t.Fatalf("unknown viewer code = %d, want 404", response.Code)
	}
	disabled := e.request(t, http.MethodPost, e.route("/api/v1/connections/"+connA.ID+"/visibility"), map[string]bool{"enabled": false}, admin)
	if disabled.Code != http.StatusOK {
		t.Fatalf("admin disable visibility = %d", disabled.Code)
	}
	if response := e.request(t, http.MethodGet, e.route("/api/v1/public/"+viewerCode(t, connA.ViewURL)), nil, ""); response.Code != http.StatusNotFound {
		t.Fatalf("disabled viewer code = %d, want 404", response.Code)
	}
}

func TestReadOnlyPublisherStatusDoesNotChangeData(t *testing.T) {
	e := newTestEnv(t)
	admin := e.setupAdmin(t)
	conn, ticket := e.createConnection(t, admin, "只读检查")
	_, publisher := e.bind(t, ticket)
	status := e.request(t, http.MethodGet, e.route("/api/v1/publisher/status"), nil, publisher)
	if status.Code != http.StatusOK {
		t.Fatalf("publisher status = %d %s", status.Code, status.Body.String())
	}
	var state struct {
		ConnectionID   string  `json:"connection_id"`
		PublicEnabled  bool    `json:"public_enabled"`
		LastReceivedAt *string `json:"last_received_at"`
	}
	decodeResponse(t, status, &state)
	if state.ConnectionID != conn.ID || state.PublicEnabled || state.LastReceivedAt != nil {
		t.Fatalf("unexpected readonly status: %#v", state)
	}
	var snapshotCount, sequence, enabled int
	if err := e.store.db.QueryRow(`SELECT count(*) FROM snapshots WHERE connection_id=?`, conn.ID).Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if err := e.store.db.QueryRow(`SELECT last_sequence,public_enabled FROM connections WHERE id=?`, conn.ID).Scan(&sequence, &enabled); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != 0 || sequence != 0 || enabled != 0 {
		t.Fatalf("read-only status mutated state: snapshots=%d sequence=%d enabled=%d", snapshotCount, sequence, enabled)
	}
}

func TestRebindStartsNewSequenceEpoch(t *testing.T) {
	e := newTestEnv(t)
	admin := e.setupAdmin(t)
	conn, ticket := e.createConnection(t, admin, "序号")
	_, oldPublisher := e.bind(t, ticket)
	first := e.postSnapshot(t, oldPublisher, makeSnapshot(9, "绑定一"))
	if first.Code != http.StatusOK {
		t.Fatal(first.Body.String())
	}
	rebound := e.request(t, http.MethodPost, e.route("/api/v1/connections/"+conn.ID+"/binding"), nil, admin)
	if rebound.Code != http.StatusOK {
		t.Fatalf("rebind failed: %d %s", rebound.Code, rebound.Body.String())
	}
	oldWriter := e.postSnapshot(t, oldPublisher, makeSnapshot(10, "旧发布者"))
	if oldWriter.Code != http.StatusUnauthorized {
		t.Fatalf("former publisher status after rebind = %d, want 401", oldWriter.Code)
	}
	var result struct {
		BindingURL string `json:"binding_url"`
	}
	decodeResponse(t, rebound, &result)
	_, newPublisher := e.bind(t, ticketFromLink(t, result.BindingURL, "#bind="))
	firstInEpoch := e.postSnapshot(t, newPublisher, makeSnapshot(1, "绑定二"))
	if firstInEpoch.Code != http.StatusOK {
		t.Fatalf("new binding's first snapshot failed: %d %s", firstInEpoch.Code, firstInEpoch.Body.String())
	}
	var response struct {
		Accepted bool `json:"accepted"`
	}
	decodeResponse(t, firstInEpoch, &response)
	if !response.Accepted {
		t.Fatal("new publisher sequence must start a new sequence epoch at 1")
	}
	var snapshots int
	if err := e.store.db.QueryRow(`SELECT count(*) FROM snapshots WHERE connection_id=?`, conn.ID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 {
		t.Fatalf("expected exactly the new epoch's snapshot, found %d rows", snapshots)
	}
	duplicate := e.postSnapshot(t, newPublisher, makeSnapshot(1, "重复"))
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate snapshot failed: %d %s", duplicate.Code, duplicate.Body.String())
	}
	decodeResponse(t, duplicate, &response)
	if response.Accepted {
		t.Fatal("duplicate sequence within the new binding must be ignored")
	}
}

func TestSnapshotAllowlistBoundsAndBasePath(t *testing.T) {
	e := newTestEnv(t)
	admin := e.setupAdmin(t)
	_, ticket := e.createConnection(t, admin, "字段检查")
	_, publisher := e.bind(t, ticket)
	valid, err := json.Marshal(makeSnapshot(1, "小满"))
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(bytes.TrimSuffix(valid, []byte("}")), []byte(`,"qq_id":"private"}`)...)
	bad := e.request(t, http.MethodPost, e.route("/api/v1/publisher/snapshot"), json.RawMessage(unknown), publisher)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown root field status = %d, want 400", bad.Code)
	}
	nested := json.RawMessage(`{"schema_version":1,"sequence":2,"generated_at":"2026-11-11T00:00:00Z","source_updated_at":null,"actor":{"name":"小满","avatar_url":"","qq_id":"private"},"life":{"enabled":true,"paused":false,"activity":null},"states":[],"schedule":null,"location":null,"sections":{"states":false,"schedule":false,"location":false}}`)
	bad = e.request(t, http.MethodPost, e.route("/api/v1/publisher/snapshot"), nested, publisher)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown nested field status = %d, want 400", bad.Code)
	}
	objectValue := json.RawMessage(`{"schema_version":1,"sequence":3,"generated_at":"2026-11-11T00:00:00Z","source_updated_at":null,"actor":{"name":"小满","avatar_url":""},"life":{"enabled":true,"paused":false,"activity":null},"states":[{"id":"x","name":"状态","category":"","type":"object","value":{"qq_id":"private"},"min":null,"max":null,"unit":"","stage":"","stage_description":"","show_value":true,"computed":false}],"schedule":null,"location":null,"sections":{"states":true,"schedule":false,"location":false}}`)
	bad = e.request(t, http.MethodPost, e.route("/api/v1/publisher/snapshot"), objectValue, publisher)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("object-valued state status = %d, want 400", bad.Code)
	}
	if response := e.postSnapshot(t, publisher, makeSnapshot(1, "公开角色")); response.Code != http.StatusOK {
		t.Fatalf("valid snapshot rejected after invalid fields: %d %s", response.Code, response.Body.String())
	}
	if response := e.setPublisherVisibility(t, publisher, true); response.Code != http.StatusOK {
		t.Fatalf("set visibility failed: %d %s", response.Code, response.Body.String())
	}
	var viewCode string
	if err := e.store.db.QueryRow(`SELECT view_code FROM connections WHERE name=?`, "字段检查").Scan(&viewCode); err != nil {
		t.Fatal(err)
	}
	public := e.request(t, http.MethodGet, e.route("/api/v1/public/"+viewCode), nil, "")
	if public.Code != http.StatusOK {
		t.Fatalf("public snapshot status = %d", public.Code)
	}
	if strings.Contains(public.Body.String(), "qq_id") || strings.Contains(public.Body.String(), "private") {
		t.Fatalf("public output echoed rejected private fields: %s", public.Body.String())
	}
	tooLarge := bytes.Repeat([]byte(" "), maxSnapshotBytes+1)
	bad = e.request(t, http.MethodPost, e.route("/api/v1/publisher/snapshot"), tooLarge, publisher)
	if bad.Code != http.StatusRequestEntityTooLarge && bad.Code != http.StatusBadRequest {
		t.Fatalf("oversized body status = %d", bad.Code)
	}
	if response := e.request(t, http.MethodGet, "/api/v1/info", nil, ""); response.Code != http.StatusNotFound {
		t.Fatalf("API route outside base path = %d, want 404", response.Code)
	}
	if response := e.request(t, http.MethodGet, "/test-life/api/v1/info", nil, ""); response.Code != http.StatusOK {
		t.Fatalf("API route under base path = %d", response.Code)
	}
	if response := e.request(t, http.MethodGet, "/test-life/", nil, ""); response.Code != http.StatusOK || response.Body.String() != "index asset" {
		t.Fatalf("index response = %d %q", response.Code, response.Body.String())
	}
	assetResponse := e.request(t, http.MethodGet, "/test-life/assets/app.js", nil, "")
	if assetResponse.Code != http.StatusOK || assetResponse.Body.String() != "app asset" {
		t.Fatalf("asset response = %d %q", assetResponse.Code, assetResponse.Body.String())
	}
	if got := assetResponse.Result().Header.Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'self'") || !strings.Contains(got, "img-src 'self' https: data:") {
		t.Fatalf("CSP does not allow the frontend policy: %q", got)
	}
}

func TestDataAndSessionsPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	cfg, err := normalizeConfig(Config{DataDir: dir, Listen: "127.0.0.1:18766", PublicURL: "http://share.example.test/test-life/"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	web := fstest.MapFS{"index.html": {Data: []byte("index")}}
	e := &testEnv{app: newApp(cfg, store, web), store: store, cfg: cfg}
	admin := e.setupAdmin(t)
	conn, ticket := e.createConnection(t, admin, "持久化")
	_, publisher := e.bind(t, ticket)
	if response := e.postSnapshot(t, publisher, makeSnapshot(8, "持久角色")); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	if response := e.setPublisherVisibility(t, publisher, true); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	store2, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store2.close() })
	e2 := &testEnv{app: newApp(cfg, store2, web), store: store2, cfg: cfg}
	login := e2.request(t, http.MethodPost, e2.route("/api/v1/login"), map[string]string{"password": "correct-horse-battery"}, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login after restart = %d: %s", login.Code, login.Body.String())
	}
	if response := e2.request(t, http.MethodGet, e2.route("/api/v1/connections"), nil, admin); response.Code != http.StatusOK {
		t.Fatalf("admin session was not persistent: %d", response.Code)
	}
	if response := e2.request(t, http.MethodGet, e2.route("/api/v1/publisher/status"), nil, publisher); response.Code != http.StatusOK {
		t.Fatalf("publisher token was not persistent: %d %s", response.Code, response.Body.String())
	}
	view := e2.request(t, http.MethodGet, e2.route("/api/v1/public/"+viewerCode(t, conn.ViewURL)), nil, "")
	if view.Code != http.StatusOK || !strings.Contains(view.Body.String(), `"sequence":8`) {
		t.Fatalf("latest snapshot/public state did not persist: %d %s", view.Code, view.Body.String())
	}
}

func TestStaticAssetsSupportConditionalETagCaching(t *testing.T) {
	e := newTestEnv(t)
	assetURL := e.route("/assets/app.js")
	first := e.request(t, http.MethodGet, assetURL, nil, "")
	if first.Code != http.StatusOK || first.Body.String() != "app asset" {
		t.Fatalf("initial asset response = %d %q", first.Code, first.Body.String())
	}
	etag := first.Result().Header.Get("ETag")
	if etag == "" || first.Result().Header.Get("Cache-Control") != "public, no-cache" {
		t.Fatalf("asset cache headers = %#v", first.Result().Header)
	}

	req := httptest.NewRequest(http.MethodGet, assetURL, nil)
	req.Header.Set("If-None-Match", etag)
	notModified := httptest.NewRecorder()
	e.app.ServeHTTP(notModified, req)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("conditional asset response = %d body=%q", notModified.Code, notModified.Body.String())
	}
	if got := notModified.Header().Get("ETag"); got != etag {
		t.Fatalf("304 ETag = %q, want %q", got, etag)
	}
	if got := notModified.Header().Get("Cache-Control"); got != "public, no-cache" {
		t.Fatalf("304 Cache-Control = %q", got)
	}

	web := e.app.web.(fstest.MapFS)
	web["assets/app.js"].Data = []byte("app asset changed")
	changed := e.request(t, http.MethodGet, assetURL, nil, "")
	if changed.Code != http.StatusOK || changed.Body.String() != "app asset changed" {
		t.Fatalf("changed asset response = %d %q", changed.Code, changed.Body.String())
	}
	if changed.Result().Header.Get("ETag") == etag {
		t.Fatal("asset ETag did not change with its content")
	}

	index := e.request(t, http.MethodGet, e.route("/"), nil, "")
	if index.Code != http.StatusOK || index.Result().Header.Get("Cache-Control") != "no-store" || index.Result().Header.Get("ETag") != "" {
		t.Fatalf("HTML cache headers = %#v", index.Result().Header)
	}
	api := e.request(t, http.MethodGet, e.route("/api/v1/info"), nil, "")
	if api.Code != http.StatusOK || api.Result().Header.Get("Cache-Control") != "no-store" || api.Result().Header.Get("ETag") != "" {
		t.Fatalf("API cache headers = %#v", api.Result().Header)
	}
}

type blockedBody struct {
	started chan struct{}
	release chan struct{}
	data    []byte
	once    bool
}

func (b *blockedBody) Read(buffer []byte) (int, error) {
	if !b.once {
		b.once = true
		close(b.started)
		<-b.release
	}
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	n := copy(buffer, b.data)
	b.data = b.data[n:]
	return n, nil
}

func TestPublisherWritesRecheckBindingAfterBodyRead(t *testing.T) {
	for _, endpoint := range []string{"snapshot", "visibility"} {
		for _, invalidation := range []string{"rebind", "delete"} {
			t.Run(endpoint+"_"+invalidation, func(t *testing.T) {
				e := newTestEnv(t)
				admin := e.setupAdmin(t)
				conn, ticket := e.createConnection(t, admin, endpoint+" race")
				_, publisher := e.bind(t, ticket)
				bodyData := []byte(`{"enabled":true}`)
				route := e.route("/api/v1/publisher/visibility")
				if endpoint == "snapshot" {
					var err error
					bodyData, err = json.Marshal(makeSnapshot(1, "竞态测试"))
					if err != nil {
						t.Fatal(err)
					}
					route = e.route("/api/v1/publisher/snapshot")
				}
				reader := &blockedBody{started: make(chan struct{}), release: make(chan struct{}), data: bodyData}
				req := httptest.NewRequest(http.MethodPost, route, reader)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+publisher)
				response := httptest.NewRecorder()
				done := make(chan struct{})
				go func() {
					e.app.ServeHTTP(response, req)
					close(done)
				}()
				select {
				case <-reader.started:
				case <-time.After(3 * time.Second):
					t.Fatal("request did not reach body read after publisher authentication")
				}
				if invalidation == "rebind" {
					changed := e.request(t, http.MethodPost, e.route("/api/v1/connections/"+conn.ID+"/binding"), nil, admin)
					if changed.Code != http.StatusOK {
						t.Fatalf("admin rebind failed: %d %s", changed.Code, changed.Body.String())
					}
				} else {
					changed := e.request(t, http.MethodDelete, e.route("/api/v1/connections/"+conn.ID), nil, admin)
					if changed.Code != http.StatusOK {
						t.Fatalf("admin delete failed: %d %s", changed.Code, changed.Body.String())
					}
				}
				close(reader.release)
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("publisher request did not finish after body release")
				}
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("stale publisher request status = %d, want 401: %s", response.Code, response.Body.String())
				}
				if endpoint == "snapshot" {
					var count int
					if err := e.store.db.QueryRow(`SELECT count(*) FROM snapshots WHERE connection_id=?`, conn.ID).Scan(&count); err != nil {
						t.Fatal(err)
					}
					if count != 0 {
						t.Fatalf("stale publisher wrote %d snapshot rows", count)
					}
				} else if invalidation == "rebind" {
					var enabled int
					if err := e.store.db.QueryRow(`SELECT public_enabled FROM connections WHERE id=?`, conn.ID).Scan(&enabled); err != nil {
						t.Fatal(err)
					}
					if enabled != 0 {
						t.Fatalf("stale publisher reopened visibility after rebind")
					}
				}
			})
		}
	}
}

type trackedHTTPServer struct {
	server         *httptest.Server
	basePath       string
	publicInFlight atomic.Int32
}

func newTrackedHTTPServer(e *testEnv) *trackedHTTPServer {
	return newTrackedHTTPServerWithTimeout(e, 0, 0)
}

func newTrackedHTTPServerWithTimeout(e *testEnv, readTimeout, writeTimeout time.Duration) *trackedHTTPServer {
	server := &trackedHTTPServer{basePath: e.cfg.BasePath}
	server.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicPrefix := strings.TrimSuffix(server.basePath, "/") + "/api/v1/public/"
		if strings.HasPrefix(r.URL.Path, publicPrefix) {
			server.publicInFlight.Add(1)
			defer server.publicInFlight.Add(-1)
		}
		e.app.ServeHTTP(w, r)
	}))
	server.server.Config.ReadTimeout = readTimeout
	server.server.Config.WriteTimeout = writeTimeout
	server.server.Start()
	return server
}

func (s *trackedHTTPServer) apiURL(route string) string {
	return s.server.URL + strings.TrimSuffix(s.basePath, "/") + "/api/v1/" + route
}

func (s *trackedHTTPServer) publicURL(code string) string {
	return s.apiURL("public/" + code)
}

type livePublisherStream struct {
	response *http.Response
	reader   *bufio.Reader
	cancel   context.CancelFunc
}

func openLivePublisherStream(t *testing.T, server *trackedHTTPServer, publisher string) *livePublisherStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.apiURL("publisher/events"), nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+publisher)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stream := &livePublisherStream{response: response, reader: bufio.NewReader(response.Body), cancel: cancel}
	t.Cleanup(func() {
		cancel()
		_ = response.Body.Close()
		server.server.Close()
	})
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("publisher event stream status = %d: %s", response.StatusCode, body)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("event stream content type = %q", response.Header.Get("Content-Type"))
	}
	event, data, err := readSSEFrame(stream.reader)
	if err != nil || event != "" || data != "" {
		t.Fatalf("initial event frame = %q %q, err=%v", event, data, err)
	}
	return stream
}

func readSSEFrame(reader *bufio.Reader) (string, string, error) {
	var event, data string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", "", err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			return event, data, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if value, ok := strings.CutPrefix(line, "event: "); ok {
			event = value
		} else if value, ok := strings.CutPrefix(line, "data: "); ok {
			data = value
		}
	}
}

type publicResult struct {
	status int
	body   []byte
	err    error
}

func startPublicRequest(server *trackedHTTPServer, viewCode string) <-chan publicResult {
	result := make(chan publicResult, 1)
	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		response, err := client.Get(server.publicURL(viewCode))
		if err != nil {
			result <- publicResult{err: err}
			return
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		result <- publicResult{status: response.StatusCode, body: body, err: readErr}
	}()
	return result
}

func awaitPublicResult(t *testing.T, result <-chan publicResult) publicResult {
	t.Helper()
	select {
	case value := <-result:
		if value.err != nil {
			t.Fatal(value.err)
		}
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("public request did not finish")
		return publicResult{}
	}
}

func waitForPending(t *testing.T, e *testEnv, connectionID string) *pendingRefresh {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.app.streamMu.Lock()
		stream := e.app.streams[connectionID]
		if stream != nil && stream.pending != nil {
			pending := stream.pending
			e.app.streamMu.Unlock()
			return pending
		}
		e.app.streamMu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("public request did not create a pending refresh")
	return nil
}

func TestPublicRefreshRunsOnlyOnVisitAndConcurrentGetsMerge(t *testing.T) {
	e := newTestEnv(t)
	admin := e.setupAdmin(t)
	conn, ticket := e.createConnection(t, admin, "按访问刷新")
	_, publisher := e.bind(t, ticket)
	if response := e.setPublisherVisibility(t, publisher, true); response.Code != http.StatusOK {
		t.Fatalf("enable visibility failed: %d %s", response.Code, response.Body.String())
	}
	info := e.request(t, http.MethodGet, e.route("/api/v1/info"), nil, "")
	var infoBody map[string]any
	decodeResponse(t, info, &infoBody)
	if infoBody["service_version"] != "1.0.0" || infoBody["refresh_mode"] != "on_request" {
		t.Fatalf("unexpected service refresh info: %#v", infoBody)
	}
	server := newTrackedHTTPServer(e)
	eventStream := openLivePublisherStream(t, server, publisher)
	_ = eventStream
	e.app.streamMu.Lock()
	stream := e.app.streams[conn.ID]
	if stream == nil || stream.pending != nil || len(stream.events) != 0 {
		e.app.streamMu.Unlock()
		t.Fatalf("connecting an idle subscriber triggered a refresh")
	}
	e.app.streamMu.Unlock()
	var snapshotCount int
	if err := e.store.db.QueryRow(`SELECT count(*) FROM snapshots WHERE connection_id=?`, conn.ID).Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != 0 {
		t.Fatalf("idle event stream unexpectedly published %d snapshots", snapshotCount)
	}
	first := startPublicRequest(server, viewerCode(t, conn.ViewURL))
	second := startPublicRequest(server, viewerCode(t, conn.ViewURL))
	deadline := time.Now().Add(2 * time.Second)
	for server.publicInFlight.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if server.publicInFlight.Load() != 2 {
		t.Fatalf("concurrent public requests in flight = %d, want 2", server.publicInFlight.Load())
	}
	pending := waitForPending(t, e, conn.ID)
	time.Sleep(15 * time.Millisecond)
	e.app.streamMu.Lock()
	if e.app.streams[conn.ID].pending != pending || len(e.app.streams[conn.ID].events) > 1 {
		e.app.streamMu.Unlock()
		t.Fatal("concurrent public requests did not merge into one pending refresh")
	}
	e.app.streamMu.Unlock()
	event, data, err := readSSEFrame(eventStream.reader)
	if err != nil || event != "refresh" || data != `{}` {
		t.Fatalf("refresh event = %q %q, err=%v", event, data, err)
	}
	posted := e.postSnapshot(t, publisher, makeSnapshot(1, "访客触发"))
	if posted.Code != http.StatusOK {
		t.Fatalf("snapshot post failed: %d %s", posted.Code, posted.Body.String())
	}
	for _, result := range []publicResult{awaitPublicResult(t, first), awaitPublicResult(t, second)} {
		if result.status != http.StatusOK {
			t.Fatalf("public response = %d %s", result.status, result.body)
		}
		var body struct {
			Snapshot  Snapshot `json:"snapshot"`
			Refreshed bool     `json:"refreshed"`
		}
		if err := json.Unmarshal(result.body, &body); err != nil {
			t.Fatal(err)
		}
		if !body.Refreshed || body.Snapshot.Actor.Name != "访客触发" {
			t.Fatalf("public response did not contain the accepted visit refresh: %#v", body)
		}
	}
}

func TestPublicReadWithoutSubscriberReturnsExistingSnapshot(t *testing.T) {
	e := newTestEnv(t)
	admin := e.setupAdmin(t)
	conn, ticket := e.createConnection(t, admin, "无订阅者")
	_, publisher := e.bind(t, ticket)
	if response := e.postSnapshot(t, publisher, makeSnapshot(1, "已有")); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	if response := e.setPublisherVisibility(t, publisher, true); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	server := newTrackedHTTPServer(e)
	defer server.server.Close()
	response := e.request(t, http.MethodGet, e.route("/api/v1/public/"+viewerCode(t, conn.ViewURL)), nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("public fallback = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Refreshed bool     `json:"refreshed"`
		Snapshot  Snapshot `json:"snapshot"`
	}
	decodeResponse(t, response, &body)
	if body.Refreshed || body.Snapshot.Actor.Name != "已有" {
		t.Fatalf("without a subscriber, service should return the saved snapshot: %#v", body)
	}
	if server.publicInFlight.Load() != 0 {
		t.Fatal("direct fallback unexpectedly waited for a publisher")
	}
}

func TestPublisherEventStreamOutlivesHTTPWriteTimeout(t *testing.T) {
	e := newTestEnv(t)
	admin := e.setupAdmin(t)
	conn, ticket := e.createConnection(t, admin, "长连接")
	_, publisher := e.bind(t, ticket)
	if response := e.setPublisherVisibility(t, publisher, true); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	server := newTrackedHTTPServerWithTimeout(e, 50*time.Millisecond, 50*time.Millisecond)
	eventStream := openLivePublisherStream(t, server, publisher)
	time.Sleep(100 * time.Millisecond)
	public := startPublicRequest(server, viewerCode(t, conn.ViewURL))
	if event, data, err := readSSEFrame(eventStream.reader); err != nil || event != "refresh" || data != `{}` {
		t.Fatalf("refresh after idle stream = %q %q, err=%v", event, data, err)
	}
	if response := e.postSnapshot(t, publisher, makeSnapshot(1, "长连接正常")); response.Code != http.StatusOK {
		t.Fatalf("snapshot upload after idle stream failed: %d %s", response.Code, response.Body.String())
	}
	result := awaitPublicResult(t, public)
	if result.status != http.StatusOK || !strings.Contains(string(result.body), `"refreshed":true`) {
		t.Fatalf("public response after idle event stream = %d %s", result.status, result.body)
	}
}

func TestVisibilityCloseNotifiesAndWakesPublicRead(t *testing.T) {
	for _, useAdmin := range []bool{false, true} {
		name := "publisher"
		if useAdmin {
			name = "admin"
		}
		t.Run(name, func(t *testing.T) {
			e := newTestEnv(t)
			admin := e.setupAdmin(t)
			conn, ticket := e.createConnection(t, admin, "关闭分享")
			_, publisher := e.bind(t, ticket)
			if response := e.setPublisherVisibility(t, publisher, true); response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			server := newTrackedHTTPServer(e)
			eventStream := openLivePublisherStream(t, server, publisher)
			public := startPublicRequest(server, viewerCode(t, conn.ViewURL))
			_ = waitForPending(t, e, conn.ID)
			if event, data, err := readSSEFrame(eventStream.reader); err != nil || event != "refresh" || data != `{}` {
				t.Fatalf("initial refresh event = %q %q, err=%v", event, data, err)
			}
			var closed *httptest.ResponseRecorder
			if useAdmin {
				closed = e.request(t, http.MethodPost, e.route("/api/v1/connections/"+conn.ID+"/visibility"), map[string]bool{"enabled": false}, admin)
			} else {
				closed = e.setPublisherVisibility(t, publisher, false)
			}
			if closed.Code != http.StatusOK {
				t.Fatalf("visibility close failed: %d %s", closed.Code, closed.Body.String())
			}
			if event, data, err := readSSEFrame(eventStream.reader); err != nil || event != "visibility" || data != `{"enabled":false}` {
				t.Fatalf("visibility event = %q %q, err=%v", event, data, err)
			}
			result := awaitPublicResult(t, public)
			if result.status != http.StatusNotFound {
				t.Fatalf("public read after close = %d: %s", result.status, result.body)
			}
		})
	}
}

func TestRebindAndDeleteClosePublisherEventStream(t *testing.T) {
	for _, operation := range []string{"rebind", "delete"} {
		t.Run(operation, func(t *testing.T) {
			e := newTestEnv(t)
			admin := e.setupAdmin(t)
			conn, ticket := e.createConnection(t, admin, "撤销流")
			_, publisher := e.bind(t, ticket)
			server := newTrackedHTTPServer(e)
			eventStream := openLivePublisherStream(t, server, publisher)
			readDone := make(chan error, 1)
			go func() {
				_, err := io.Copy(io.Discard, eventStream.response.Body)
				readDone <- err
			}()
			if operation == "rebind" {
				response := e.request(t, http.MethodPost, e.route("/api/v1/connections/"+conn.ID+"/binding"), nil, admin)
				if response.Code != http.StatusOK {
					t.Fatalf("rebind failed: %d %s", response.Code, response.Body.String())
				}
			} else {
				response := e.request(t, http.MethodDelete, e.route("/api/v1/connections/"+conn.ID), nil, admin)
				if response.Code != http.StatusOK {
					t.Fatalf("delete failed: %d %s", response.Code, response.Body.String())
				}
			}
			select {
			case <-readDone:
			case <-time.After(2 * time.Second):
				t.Fatal("publisher event stream remained open after invalidation")
			}
			oldStatus := e.request(t, http.MethodGet, e.route("/api/v1/publisher/status"), nil, publisher)
			if oldStatus.Code != http.StatusUnauthorized {
				t.Fatalf("old publisher token after %s = %d", operation, oldStatus.Code)
			}
		})
	}
}
