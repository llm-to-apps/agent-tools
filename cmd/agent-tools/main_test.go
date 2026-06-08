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

	var readResp map[string]string
	decode(t, readRec, &readResp)
	if readResp["content"] != "hello tools" {
		t.Fatalf("unexpected content: %q", readResp["content"])
	}

	treeReq := httptest.NewRequest(http.MethodGet, "/files/tree?path=.&maxDepth=2", nil)
	treeRec := httptest.NewRecorder()
	s.filesTree(treeRec, treeReq)
	assertStatus(t, treeRec, http.StatusOK)

	if !strings.Contains(treeRec.Body.String(), "docs/hello.txt") {
		t.Fatalf("tree response does not include written file: %s", treeRec.Body.String())
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

func TestReadJSONRejectsInvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/shell/run", bytes.NewBufferString("{"))
	var payload map[string]string
	if err := readJSON(req, &payload); err == nil {
		t.Fatal("expected invalid JSON to return an error")
	}
}
