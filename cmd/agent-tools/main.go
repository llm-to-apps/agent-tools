package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type server struct {
	workdir string
	app     *appProcess
}

type jsonError struct {
	Error string `json:"error"`
}

type commandResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Duration string `json:"duration"`
}

type appProcess struct {
	mu      sync.Mutex
	command string
	workdir string
	logPath string
	cmd     *exec.Cmd
	done    chan error
	started *time.Time
}

func main() {
	workdir := env("AGENT_WORKDIR", "/workspace")
	host := env("AGENT_TOOLS_HOST", "0.0.0.0")
	port := env("AGENT_TOOLS_PORT", "7070")
	logPath := env("AGENT_APP_LOG", "/tmp/agent-tools-app.log")
	token := os.Getenv("AGENT_TOOLS_TOKEN")

	s := &server{
		workdir: workdir,
		app: &appProcess{
			command: os.Getenv("APP_COMMAND"),
			workdir: workdir,
			logPath: logPath,
		},
	}

	if startupCommands := os.Getenv("APP_STARTUP_COMMANDS"); startupCommands != "" {
		if err := runLoggedCommand(workdir, startupCommands, logPath, envDurationSeconds("APP_STARTUP_TIMEOUT_SECONDS", 120)); err != nil {
			log.Printf("startup command failed: %v", err)
			os.Exit(1)
		}
	}

	if s.app.command != "" {
		if err := s.app.start(); err != nil {
			log.Printf("failed to start app command: %v", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /files/tree", s.filesTree)
	mux.HandleFunc("GET /files/read", s.filesRead)
	mux.HandleFunc("POST /files/write", s.filesWrite)
	mux.HandleFunc("POST /files/replace-text", s.filesReplaceText)
	mux.HandleFunc("POST /files/patch", s.filesPatch)
	mux.HandleFunc("POST /shell/run", s.shellRun)
	mux.HandleFunc("GET /git/status", s.gitStatus)
	mux.HandleFunc("GET /git/diff", s.gitDiff)
	mux.HandleFunc("POST /git/commit", s.gitCommit)
	mux.HandleFunc("POST /app/start", s.appStart)
	mux.HandleFunc("POST /app/restart", s.appRestart)
	mux.HandleFunc("GET /app/status", s.appStatus)
	mux.HandleFunc("GET /app/logs", s.appLogs)

	addr := host + ":" + port
	log.Printf("agent-tools listening on %s, workdir=%s", addr, workdir)
	if err := http.ListenAndServe(addr, withJSON(withAuth(mux, token))); err != nil {
		log.Fatal(err)
	}
}

func withAuth(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		headerToken := r.Header.Get("X-Agent-Tools-Token")
		if auth != token && headerToken != token {
			writeError(w, http.StatusUnauthorized, errors.New("unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"workdir": s.workdir,
		"app":     s.app.status(),
	})
}

func (s *server) filesTree(w http.ResponseWriter, r *http.Request) {
	root, err := s.safePath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	maxDepth := parseInt(r.URL.Query().Get("maxDepth"), 3)
	if maxDepth < 0 || maxDepth > 12 {
		maxDepth = 3
	}

	type fileEntry struct {
		Path  string `json:"path"`
		Type  string `json:"type"`
		Size  int64  `json:"size"`
		Depth int    `json:"depth"`
	}

	var entries []fileEntry
	baseDepth := strings.Count(root, string(os.PathSeparator))
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		depth := strings.Count(path, string(os.PathSeparator)) - baseDepth
		if depth > maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if depth == 0 {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		kind := "file"
		if d.IsDir() {
			kind = "dir"
		}
		entries = append(entries, fileEntry{
			Path:  s.rel(path),
			Type:  kind,
			Size:  info.Size(),
			Depth: depth,
		})
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *server) filesRead(w http.ResponseWriter, r *http.Request) {
	path, err := s.safePath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	startLine := parseInt(r.URL.Query().Get("startLine"), 0)
	endLine := parseInt(r.URL.Query().Get("endLine"), 0)
	content, actualStart, actualEnd, totalLines, err := sliceLines(string(data), startLine, endLine)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      s.rel(path),
		"content":   content,
		"startLine": actualStart,
		"endLine":   actualEnd,
		"totalLines": totalLines,
	})
}

func (s *server) filesWrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Append  bool   `json:"append"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	path, err := s.safePath(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if req.Append {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	file, err := os.OpenFile(path, flag, 0644)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer file.Close()
	if _, err := file.WriteString(req.Content); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": s.rel(path)})
}

func (s *server) filesReplaceText(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path                 string `json:"path"`
		Search               string `json:"search"`
		Replace              string `json:"replace"`
		ExpectedReplacements int    `json:"expectedReplacements"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Search == "" {
		writeError(w, http.StatusBadRequest, errors.New("search is required"))
		return
	}
	path, err := s.safePath(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	content := string(data)
	replacements := strings.Count(content, req.Search)
	if replacements == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":        "search text not found",
			"path":         s.rel(path),
			"replacements": 0,
		})
		return
	}
	if req.ExpectedReplacements > 0 && replacements != req.ExpectedReplacements {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":                 "replacement count did not match expectedReplacements",
			"path":                  s.rel(path),
			"replacements":          replacements,
			"expectedReplacements":  req.ExpectedReplacements,
		})
		return
	}

	updated := strings.ReplaceAll(content, req.Search, req.Replace)
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":         s.rel(path),
		"replacements": replacements,
	})
}

func (s *server) filesPatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Patch string `json:"patch"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Patch) == "" {
		writeError(w, http.StatusBadRequest, errors.New("patch is required"))
		return
	}
	result := runCommand(s.workdir, "git", []string{"apply", "--verbose", "--whitespace=nowarn", "-"}, nil, req.Patch, 30*time.Second)
	if result.ExitCode != 0 {
		fallback := runCommand(s.workdir, "patch", []string{"-p1"}, nil, req.Patch, 30*time.Second)
		if fallback.ExitCode == 0 {
			writeJSON(w, http.StatusOK, fallback)
			return
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "patch could not be applied",
			"gitApply": result,
			"patch":    fallback,
		})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) shellRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command        string            `json:"command"`
		Args           []string          `json:"args"`
		Cwd            string            `json:"cwd"`
		Env            map[string]string `json:"env"`
		TimeoutSeconds int               `json:"timeoutSeconds"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		writeError(w, http.StatusBadRequest, errors.New("command is required"))
		return
	}

	cwd, err := s.safePath(req.Cwd)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > 300*time.Second {
		timeout = 60 * time.Second
	}

	var result commandResult
	if len(req.Args) > 0 {
		result = runCommand(cwd, req.Command, req.Args, req.Env, "", timeout)
	} else {
		result = runCommand(cwd, "sh", []string{"-lc", req.Command}, req.Env, "", timeout)
	}
	status := http.StatusOK
	if result.ExitCode != 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, result)
}

func (s *server) gitStatus(w http.ResponseWriter, r *http.Request) {
	result := runCommand(s.workdir, "git", []string{"status", "--short", "--branch"}, nil, "", 30*time.Second)
	writeJSON(w, http.StatusOK, result)
}

func (s *server) gitDiff(w http.ResponseWriter, r *http.Request) {
	result := runCommand(s.workdir, "git", []string{"diff", "--", "."}, nil, "", 30*time.Second)
	writeJSON(w, http.StatusOK, result)
}

func (s *server) gitCommit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
		All     bool   `json:"all"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusBadRequest, errors.New("message is required"))
		return
	}
	if req.All {
		add := runCommand(s.workdir, "git", []string{"add", "-A"}, nil, "", 30*time.Second)
		if add.ExitCode != 0 {
			writeJSON(w, http.StatusBadRequest, add)
			return
		}
	}
	commit := runCommand(s.workdir, "git", []string{"commit", "-m", req.Message}, nil, "", 60*time.Second)
	status := http.StatusOK
	if commit.ExitCode != 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, commit)
}

func (s *server) appStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string `json:"command"`
	}
	if err := readJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Command) != "" {
		s.app.setCommand(req.Command)
	}
	if err := s.app.start(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.app.status())
}

func (s *server) appRestart(w http.ResponseWriter, r *http.Request) {
	if err := s.app.restart(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.app.status())
}

func (s *server) appStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.status())
}

func (s *server) appLogs(w http.ResponseWriter, r *http.Request) {
	tail := parseInt(r.URL.Query().Get("tail"), 200)
	if tail <= 0 || tail > 2000 {
		tail = 200
	}
	lines, err := tailFile(s.app.logPath, tail)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":  s.app.logPath,
		"lines": lines,
	})
}

func (a *appProcess) setCommand(command string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.command = command
}

func (a *appProcess) start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd != nil && a.cmd.Process != nil && a.cmd.ProcessState == nil {
		return errors.New("app is already running")
	}
	if strings.TrimSpace(a.command) == "" {
		return errors.New("app command is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(a.logPath), 0755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(a.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	cmd := exec.Command("sh", "-lc", a.command)
	cmd.Dir = a.workdir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	now := time.Now()
	a.cmd = cmd
	a.done = make(chan error, 1)
	a.started = &now

	go func() {
		err := cmd.Wait()
		a.done <- err
		if err != nil {
			log.Printf("app process exited: %v", err)
		}
		_ = logFile.Close()
	}()

	return nil
}

func (a *appProcess) restart() error {
	a.mu.Lock()
	cmd := a.cmd
	done := a.done
	a.mu.Unlock()

	if cmd != nil && cmd.Process != nil && cmd.ProcessState == nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			if cmd.ProcessState == nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				return errors.New("app process did not stop")
			}
		}
	}
	return a.start()
}

func (a *appProcess) status() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	running := false
	pid := 0
	if a.cmd != nil && a.cmd.Process != nil && a.cmd.ProcessState == nil {
		running = true
		pid = a.cmd.Process.Pid
	}
	var started any
	if a.started != nil {
		started = a.started.Format(time.RFC3339)
	}
	return map[string]any{
		"running": running,
		"pid":     pid,
		"command": a.command,
		"started": started,
		"logPath": a.logPath,
	}
}

func (s *server) safePath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "."
	}
	if filepath.IsAbs(raw) {
		return "", errors.New("absolute paths are not allowed")
	}
	candidate := filepath.Clean(filepath.Join(s.workdir, raw))
	base := filepath.Clean(s.workdir)
	if candidate != base && !strings.HasPrefix(candidate, base+string(os.PathSeparator)) {
		return "", errors.New("path escapes workdir")
	}
	return candidate, nil
}

func (s *server) rel(path string) string {
	rel, err := filepath.Rel(s.workdir, path)
	if err != nil {
		return path
	}
	if rel == "." {
		return ""
	}
	return rel
}

func sliceLines(content string, startLine, endLine int) (string, int, int, int, error) {
	lines := strings.SplitAfter(content, "\n")
	if content == "" {
		lines = []string{}
	} else if strings.HasSuffix(content, "\n") && len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	} else if !strings.HasSuffix(content, "\n") && len(lines) > 0 {
		lines[len(lines)-1] = strings.TrimSuffix(lines[len(lines)-1], "\n")
	}

	totalLines := len(lines)
	if startLine == 0 && endLine == 0 {
		return content, 1, totalLines, totalLines, nil
	}
	if startLine <= 0 {
		startLine = 1
	}
	if endLine <= 0 || endLine > totalLines {
		endLine = totalLines
	}
	if startLine > endLine || startLine > totalLines {
		return "", 0, 0, totalLines, errors.New("line range is outside file")
	}

	return strings.Join(lines[startLine-1:endLine], ""), startLine, endLine, totalLines, nil
}

func runCommand(cwd, command string, args []string, env map[string]string, stdin string, timeout time.Duration) commandResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		if ctx.Err() == context.DeadlineExceeded {
			stderr.WriteString("\ncommand timed out")
			exitCode = 124
		}
	}

	return commandResult{
		Command:  strings.Join(append([]string{command}, args...), " "),
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start).String(),
	}
}

func runLoggedCommand(cwd, command, logPath string, timeout time.Duration) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	fmt.Fprintf(logFile, "\n$ %s\n", command)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	cmd.Dir = cwd
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("command timed out after %s", timeout)
	}
	return err
}

func readJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("failed to write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, jsonError{Error: err.Error()})
}

func env(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func parseInt(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDurationSeconds(key string, fallback int) time.Duration {
	return time.Duration(parseInt(os.Getenv(key), fallback)) * time.Second
}

func tailFile(path string, maxLines int) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read log: %w", err)
	}
	return lines, nil
}
