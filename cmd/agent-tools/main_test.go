package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSafePathRejectsEscapes(t *testing.T) {
	tmp := t.TempDir()
	s := &server{workdir: tmp}

	path, err := s.safePath("nested/file.txt")
	if err != nil {
		t.Fatalf("safePath returned error: %v", err)
	}
	if path != filepath.Join(tmp, "nested/file.txt") {
		t.Fatalf("unexpected path: %s", path)
	}

	if _, err := s.safePath("../outside.txt"); err == nil {
		t.Fatal("expected escape path to be rejected")
	}

	if _, err := s.safePath(filepath.Join(string(os.PathSeparator), "tmp", "file.txt")); err == nil {
		t.Fatal("expected absolute path to be rejected")
	}
}

func TestFilesWriteReadAndTree(t *testing.T) {
	tmp := t.TempDir()
	s := testServer(tmp)

	writeBody := `{"path":"docs/hello.txt","content":"hello tools"}`
	writeReq := httptest.NewRequest(http.MethodPost, "/files/write", strings.NewReader(writeBody))
	writeRec := httptest.NewRecorder()
	s.filesWrite(writeRec, writeReq)
	assertStatus(t, writeRec, http.StatusOK)

	readReq := httptest.NewRequest(http.MethodGet, "/files/read?path=docs/hello.txt", nil)
	readRec := httptest.NewRecorder()
	s.filesRead(readRec, readReq)
	assertStatus(t, readRec, http.StatusOK)

	var readResp struct {
		Content string `json:"content"`
	}
	decode(t, readRec, &readResp)
	if readResp.Content != "hello tools" {
		t.Fatalf("unexpected content: %q", readResp.Content)
	}

	treeReq := httptest.NewRequest(http.MethodGet, "/files/tree?path=.&maxDepth=2", nil)
	treeRec := httptest.NewRecorder()
	s.filesTree(treeRec, treeReq)
	assertStatus(t, treeRec, http.StatusOK)

	if !strings.Contains(treeRec.Body.String(), "docs/hello.txt") {
		t.Fatalf("tree response does not include written file: %s", treeRec.Body.String())
	}
}

func TestFilesReadSupportsLineRange(t *testing.T) {
	tmp := t.TempDir()
	s := testServer(tmp)
	if err := os.WriteFile(filepath.Join(tmp, "app.txt"), []byte("one\ntwo\nthree\n"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/files/read?path=app.txt&startLine=2&endLine=3", nil)
	rec := httptest.NewRecorder()
	s.filesRead(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp struct {
		Content    string `json:"content"`
		StartLine  int    `json:"startLine"`
		EndLine    int    `json:"endLine"`
		TotalLines int    `json:"totalLines"`
	}
	decode(t, rec, &resp)
	if resp.Content != "two\nthree\n" || resp.StartLine != 2 || resp.EndLine != 3 || resp.TotalLines != 3 {
		t.Fatalf("unexpected ranged read response: %+v", resp)
	}
}

func TestFilesReplaceText(t *testing.T) {
	tmp := t.TempDir()
	s := testServer(tmp)
	if err := os.WriteFile(filepath.Join(tmp, "app.txt"), []byte("Money\nMoney\n"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	body := `{"path":"app.txt","search":"Money","replace":"Anton","expectedReplacements":2}`
	req := httptest.NewRequest(http.MethodPost, "/files/replace-text", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.filesReplaceText(rec, req)
	assertStatus(t, rec, http.StatusOK)

	data, err := os.ReadFile(filepath.Join(tmp, "app.txt"))
	if err != nil {
		t.Fatalf("read replaced file: %v", err)
	}
	if string(data) != "Anton\nAnton\n" {
		t.Fatalf("unexpected replaced content: %q", string(data))
	}
}

func TestFilesReplaceTextReturnsConflictWhenExpectedCountDiffers(t *testing.T) {
	tmp := t.TempDir()
	s := testServer(tmp)
	if err := os.WriteFile(filepath.Join(tmp, "app.txt"), []byte("Money\nMoney\n"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	body := `{"path":"app.txt","search":"Money","replace":"Anton","expectedReplacements":1}`
	req := httptest.NewRequest(http.MethodPost, "/files/replace-text", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.filesReplaceText(rec, req)
	assertStatus(t, rec, http.StatusConflict)

	data, err := os.ReadFile(filepath.Join(tmp, "app.txt"))
	if err != nil {
		t.Fatalf("read original file: %v", err)
	}
	if string(data) != "Money\nMoney\n" {
		t.Fatalf("conflicting replace changed file: %q", string(data))
	}
}

func TestShellRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell endpoint uses sh")
	}

	tmp := t.TempDir()
	s := testServer(tmp)

	body := `{"command":"printf hello","timeoutSeconds":5}`
	req := httptest.NewRequest(http.MethodPost, "/shell/run", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.shellRun(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp commandResult
	decode(t, rec, &resp)
	if resp.ExitCode != 0 || resp.Stdout != "hello" {
		t.Fatalf("unexpected shell result: %+v", resp)
	}
}

func TestFilesPatchAppliesUnifiedDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	tmp := t.TempDir()
	s := testServer(tmp)
	if err := os.WriteFile(filepath.Join(tmp, "app.txt"), []byte("Money\n"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	body := `{"patch":"diff --git a/app.txt b/app.txt\n--- a/app.txt\n+++ b/app.txt\n@@ -1 +1 @@\n-Money\n+Marina\n"}`
	req := httptest.NewRequest(http.MethodPost, "/files/patch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.filesPatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	data, err := os.ReadFile(filepath.Join(tmp, "app.txt"))
	if err != nil {
		t.Fatalf("read patched file: %v", err)
	}
	if string(data) != "Marina\n" {
		t.Fatalf("unexpected patched content: %q", string(data))
	}
}

func TestFilesPatchReturnsConflictForInvalidPatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	tmp := t.TempDir()
	s := testServer(tmp)
	if err := os.WriteFile(filepath.Join(tmp, "app.txt"), []byte("Money\n"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	body := `{"patch":"diff --git a/app.txt b/app.txt\n--- a/app.txt\n+++ b/app.txt\n@@ -1 +1 @@\n-Unknown\n+Marina\n"}`
	req := httptest.NewRequest(http.MethodPost, "/files/patch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.filesPatch(rec, req)
	assertStatus(t, rec, http.StatusConflict)

	data, err := os.ReadFile(filepath.Join(tmp, "app.txt"))
	if err != nil {
		t.Fatalf("read original file: %v", err)
	}
	if string(data) != "Money\n" {
		t.Fatalf("invalid patch changed file: %q", string(data))
	}
}

func TestRunLoggedCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("logged command uses sh")
	}

	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "app.log")

	err := runLoggedCommand(tmp, "printf migrated > migrated.txt", logPath, 5_000_000_000)
	if err != nil {
		t.Fatalf("runLoggedCommand returned error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "migrated.txt"))
	if err != nil {
		t.Fatalf("expected command output file: %v", err)
	}
	if string(data) != "migrated" {
		t.Fatalf("unexpected file content: %q", string(data))
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("expected log file: %v", err)
	}
	if !strings.Contains(string(logData), "$ printf migrated > migrated.txt") {
		t.Fatalf("log does not include command: %q", string(logData))
	}
}

func TestAppStatus(t *testing.T) {
	tmp := t.TempDir()
	s := testServer(tmp)
	s.app.setCommand("npm run dev")

	req := httptest.NewRequest(http.MethodGet, "/app/status", nil)
	rec := httptest.NewRecorder()
	s.appStatus(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp struct {
		Running bool   `json:"running"`
		Command string `json:"command"`
		LogPath string `json:"logPath"`
	}
	decode(t, rec, &resp)

	if resp.Running {
		t.Fatal("expected app to be stopped")
	}
	if resp.Command != "npm run dev" {
		t.Fatalf("unexpected app command: %q", resp.Command)
	}
	if resp.LogPath == "" {
		t.Fatal("expected log path")
	}
}

func TestAppAutoRestartStopsAfterMaxAttempts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("app supervisor uses sh")
	}

	tmp := t.TempDir()
	app := &appProcess{
		command:           `count=$(cat count.txt 2>/dev/null || echo 0); count=$((count + 1)); echo "$count" > count.txt; exit 1`,
		workdir:           tmp,
		logPath:           filepath.Join(tmp, "app.log"),
		maxRestarts:       2,
		restartBackoff:    10 * time.Millisecond,
		supervisorEnabled: true,
	}

	if err := app.start(); err != nil {
		t.Fatalf("start returned error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		status := app.status()
		data, err := os.ReadFile(filepath.Join(tmp, "count.txt"))
		return status["restartCount"] == 2 &&
			status["running"] == false &&
			status["lastExitError"] != "" &&
			err == nil &&
			strings.TrimSpace(string(data)) == "3"
	})

	data, err := os.ReadFile(filepath.Join(tmp, "count.txt"))
	if err != nil {
		t.Fatalf("read restart count fixture: %v", err)
	}
	if strings.TrimSpace(string(data)) != "3" {
		t.Fatalf("expected initial start plus 2 restarts, got count %q", string(data))
	}
}

func TestAuthMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	handler := withJSON(withAuth(next, "secret"))

	noAuthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	noAuthRec := httptest.NewRecorder()
	handler.ServeHTTP(noAuthRec, noAuthReq)
	assertStatus(t, noAuthRec, http.StatusUnauthorized)

	authReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	authReq.Header.Set("Authorization", "Bearer secret")
	authRec := httptest.NewRecorder()
	handler.ServeHTTP(authRec, authReq)
	assertStatus(t, authRec, http.StatusOK)
}

func TestGitStatusInRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	tmp := t.TempDir()
	mustRun(t, tmp, "git", "init")
	mustRun(t, tmp, "git", "config", "user.email", "agent-tools@example.test")
	mustRun(t, tmp, "git", "config", "user.name", "Agent Tools")

	s := testServer(tmp)
	req := httptest.NewRequest(http.MethodGet, "/git/status", nil)
	rec := httptest.NewRecorder()
	s.gitStatus(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp commandResult
	decode(t, rec, &resp)
	if resp.ExitCode != 0 {
		t.Fatalf("git status failed: %+v", resp)
	}
	if !strings.Contains(resp.Stdout, "No commits yet") && !strings.Contains(resp.Stdout, "main") && !strings.Contains(resp.Stdout, "master") {
		t.Fatalf("unexpected git status output: %q", resp.Stdout)
	}
}

func TestGitDiffInRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	tmp := t.TempDir()
	mustRun(t, tmp, "git", "init")
	mustRun(t, tmp, "git", "config", "user.email", "agent-tools@example.test")
	mustRun(t, tmp, "git", "config", "user.name", "Agent Tools")
	if err := os.WriteFile(filepath.Join(tmp, "app.txt"), []byte("Money\n"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	mustRun(t, tmp, "git", "add", "app.txt")
	mustRun(t, tmp, "git", "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(tmp, "app.txt"), []byte("Anton\n"), 0644); err != nil {
		t.Fatalf("write changed fixture: %v", err)
	}

	s := testServer(tmp)
	req := httptest.NewRequest(http.MethodGet, "/git/diff", nil)
	rec := httptest.NewRecorder()
	s.gitDiff(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp commandResult
	decode(t, rec, &resp)
	if resp.ExitCode != 0 || !strings.Contains(resp.Stdout, "-Money") || !strings.Contains(resp.Stdout, "+Anton") {
		t.Fatalf("unexpected git diff result: %+v", resp)
	}
}

func TestGitSaveCommitsAndPushes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	workdir := filepath.Join(tmp, "work")
	clone := filepath.Join(tmp, "clone")

	mustRun(t, tmp, "git", "init", "--bare", remote)
	if err := os.MkdirAll(workdir, 0755); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	mustRun(t, workdir, "git", "init", "-b", "main")
	mustRun(t, workdir, "git", "config", "user.email", "agent-tools@example.test")
	mustRun(t, workdir, "git", "config", "user.name", "Agent Tools")
	mustRun(t, workdir, "git", "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(workdir, "app.txt"), []byte("Saved\n"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	s := testServer(workdir)
	req := httptest.NewRequest(http.MethodPost, "/git/save", strings.NewReader(`{"message":"Save app changes"}`))
	rec := httptest.NewRecorder()
	s.gitSave(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp commandResult
	decode(t, rec, &resp)
	if resp.ExitCode != 0 {
		t.Fatalf("git save failed: %+v", resp)
	}

	mustRun(t, tmp, "git", "clone", "--branch", "main", remote, clone)
	data, err := os.ReadFile(filepath.Join(clone, "app.txt"))
	if err != nil {
		t.Fatalf("read pushed file: %v", err)
	}
	if string(data) != "Saved\n" {
		t.Fatalf("unexpected pushed content: %q", string(data))
	}
}

func testServer(workdir string) *server {
	return &server{
		workdir: workdir,
		app: &appProcess{
			workdir: workdir,
			logPath: filepath.Join(workdir, "app.log"),
		},
	}
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if rec.Code != expected {
		t.Fatalf("expected status %d, got %d: %s", expected, rec.Code, rec.Body.String())
	}
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(target); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
}

func mustRun(t *testing.T, cwd, command string, args ...string) {
	t.Helper()
	result := runCommand(cwd, command, args, nil, "", 30_000_000_000)
	if result.ExitCode != 0 {
		t.Fatalf("%s failed: stdout=%q stderr=%q", command, result.Stdout, result.Stderr)
	}
}

func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func TestReadJSONRejectsInvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/shell/run", bytes.NewBufferString("{"))
	var payload map[string]string
	if err := readJSON(req, &payload); err == nil {
		t.Fatal("expected invalid JSON to return an error")
	}
}
