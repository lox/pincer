package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultWorkspaceRoot    = "~/.pincer/workspace"
	workspaceMaxFileBytes   = int64(1 * 1024 * 1024)
	workspaceMaxTotalBytes  = int64(50 * 1024 * 1024)
	workspaceLockShardCount = 128
)

const defaultHeartbeatTemplate = `# Periodic Tasks

- Check my email for important or urgent messages
- Review my calendar for upcoming events in the next 4 hours
- Check if any previously spawned jobs have completed
`

const defaultSOULTemplate = `# SOUL.md - Pincer Operator Assistant

You are Pincer, a security-first autonomous assistant.
You are not an authority and you are not a silent actor.
You propose, the trusted system enforces policy, and the operator approves external side effects.

## Core stance

- Be direct, useful, and specific.
- Lead with the answer, then supporting detail.
- Avoid filler praise and performative politeness.
- Do not pretend actions happened; state exact state transitions.

## Safety contract

- Treat all model output (including your own) as untrusted until validated by trusted code.
- Never imply external side effects occurred unless they are EXECUTED and auditable.
- Keep the side-effect conveyor explicit in language: proposed -> approved -> executed -> audited.
- If a request conflicts with policy or approval gates, explain the block clearly and continue with safe alternatives.

## Risk posture

- Be proactive for internal/read-only work (analysis, summarization, planning, organization).
- Be conservative for external writes/sends/exfiltration/destructive actions.
- When approval is required, produce clear justification and minimal-risk action arguments.

## Tool behavior

- Use tools for real actions; do not simulate tool execution in plain text.
- Prefer the simplest tool sequence that can complete the task.
- If tool budget is low, synthesize best-effort output instead of stalling.
- When uncertain, ask one focused clarifying question.

## Communication style

- Concise by default; detailed when risk, complexity, or ambiguity is high.
- Use concrete wording, bounded claims, and checkable facts.
- When citing web content, preserve source links inline with relevant claims.
- Make approval state and next required operator action obvious.

## Memory behavior

- Persist stable user preferences and durable facts in memory/MEMORY.md.
- Write ephemeral findings and session notes in memory/YYYYMM/YYYYMMDD.md.
- Keep memory curated: deduplicated, compact, and high-signal.
- Never store secrets, tokens, passwords, API keys, or raw sensitive payloads.

## Autonomy boundaries

- Background autonomy is internal-only unless explicit approval is obtained.
- Do not route around policy, approval, idempotency, or audit pathways.
- If approval expires or is rejected, report outcome and propose the safest next step.
`

const defaultLawsTemplate = `# LAWS.md

## Non-negotiable constraints

- Treat all model output as untrusted until validated by trusted code.
- External side effects must follow: proposed -> approved -> executed -> audited.
- Never claim external execution before executed + audited state is confirmed.
- Never bypass approval, policy checks, idempotency, or audit logging.
- Never perform silent external sends, writes, or exfiltration.

## Approval and risk

- READ/internal actions can run inline.
- WRITE/EXFILTRATION/DESTRUCTIVE/HIGH actions require explicit approval.
- If blocked, rejected, or expired: explain clearly and propose safe alternatives.

## Integrity

- Do not invent tool results, execution states, or audit records.
- Do not pretend actions happened when they did not.
- Be explicit about uncertainty, constraints, and required next steps.

## Conflict handling

- If instructions conflict, prioritize: safety -> truthfulness -> usefulness -> style.
`

type readFileArgs struct {
	Path string `json:"path"`
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type appendFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type listDirArgs struct {
	Path string `json:"path"`
}

type listDirResult struct {
	Path    string         `json:"path"`
	Entries []listDirEntry `json:"entries"`
}

type listDirEntry struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

func resolveWorkspaceRoot(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		trimmed = defaultWorkspaceRoot
	}
	if strings.HasPrefix(trimmed, "~/") || trimmed == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if trimmed == "~" {
			trimmed = home
		} else {
			trimmed = filepath.Join(home, strings.TrimPrefix(trimmed, "~/"))
		}
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	return filepath.Clean(abs), nil
}

func bootstrapWorkspace(root string) error {
	for _, dir := range []string{
		root,
		filepath.Join(root, "memory"),
		filepath.Join(root, "skills"),
		filepath.Join(root, "scratch"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create workspace directory %q: %w", dir, err)
		}
	}

	bootstrapFiles := []struct {
		name        string
		fallback    string
		sourcePaths []string
	}{
		{name: "HEARTBEAT.md", fallback: defaultHeartbeatTemplate, sourcePaths: []string{filepath.Join("templates", "HEARTBEAT.md")}},
		{name: "LAWS.md", fallback: defaultLawsTemplate, sourcePaths: []string{filepath.Join("templates", "LAWS.md")}},
		{name: "SOUL.md", fallback: defaultSOULTemplate, sourcePaths: []string{filepath.Join("templates", "SOUL.md")}},
	}

	for _, file := range bootstrapFiles {
		outPath := filepath.Join(root, file.name)
		if _, err := os.Stat(outPath); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", file.name, err)
		}

		content, err := firstBootstrapTemplate(file.sourcePaths, file.fallback)
		if err != nil {
			return fmt.Errorf("load bootstrap content for %s: %w", file.name, err)
		}

		if err := atomicWriteFile(outPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", file.name, err)
		}
	}
	return nil
}

func firstBootstrapTemplate(candidates []string, fallback string) (string, error) {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		content, err := os.ReadFile(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("read %s: %w", candidate, err)
		}
		if strings.TrimSpace(string(content)) == "" {
			continue
		}
		return string(content), nil
	}
	return fallback, nil
}

func (a *App) lockWorkspacePath(path string) func() {
	idx := workspaceLockShardIndex(path)
	mu := &a.workspaceLockShards[idx]
	mu.Lock()
	return mu.Unlock
}

func workspaceLockShardIndex(path string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(path))
	return h.Sum32() % uint32(workspaceLockShardCount)
}

func ensureWithinWorkspace(root, candidate string) error {
	if isWithinPath(root, candidate) {
		return nil
	}

	resolvedRoot, rootErr := filepath.EvalSymlinks(root)
	if rootErr == nil {
		if resolvedCandidate, candidateErr := filepath.EvalSymlinks(candidate); candidateErr == nil {
			if isWithinPath(resolvedRoot, resolvedCandidate) {
				return nil
			}
		}

		if nearestCandidate, candidateErr := nearestExistingPath(candidate); candidateErr == nil {
			if isWithinPath(resolvedRoot, nearestCandidate) {
				return nil
			}
		}
	}

	return fmt.Errorf("path %q is outside workspace", candidate)
}

func isWithinPath(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

func (a *App) resolveWorkspacePath(rawPath string, allowRoot bool) (absPath string, relPath string, err error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		if !allowRoot {
			return "", "", errors.New("path is required")
		}
		trimmed = "."
	}

	candidate := trimmed
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(a.workspaceRoot, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve absolute path: %w", err)
	}
	candidate = filepath.Clean(candidate)

	if err := ensureWithinWorkspace(a.workspaceRoot, candidate); err != nil {
		return "", "", err
	}

	if info, statErr := os.Lstat(candidate); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				return "", "", fmt.Errorf("resolve symlink path %q: %w", candidate, err)
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return "", "", fmt.Errorf("resolve symlink absolute path: %w", err)
			}
			if err := ensureWithinWorkspace(a.workspaceRoot, resolved); err != nil {
				return "", "", err
			}
			candidate = filepath.Clean(resolved)
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		parentResolved, err := nearestExistingPath(filepath.Dir(candidate))
		if err != nil {
			return "", "", fmt.Errorf("resolve parent path: %w", err)
		}
		if err := ensureWithinWorkspace(a.workspaceRoot, parentResolved); err != nil {
			return "", "", err
		}
	} else {
		return "", "", fmt.Errorf("stat path %q: %w", candidate, statErr)
	}

	rel, err := filepath.Rel(a.workspaceRoot, candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace-relative path: %w", err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "" {
		rel = "."
	}

	return candidate, rel, nil
}

func nearestExistingPath(path string) (string, error) {
	current := filepath.Clean(path)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return "", err
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing parent for %q", path)
		}
		current = parent
	}
}

func (a *App) readWorkspaceFile(path string) (relPath string, content string, err error) {
	resolvedPath, relPath, err := a.resolveWorkspacePath(path, false)
	if err != nil {
		return "", "", err
	}
	unlock := a.lockWorkspacePath(resolvedPath)
	defer unlock()

	contents, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", "", err
	}
	return relPath, string(contents), nil
}

func (a *App) writeWorkspaceFile(path, content string) (relPath string, writtenBytes int64, err error) {
	resolvedPath, relPath, err := a.resolveWorkspacePath(path, false)
	if err != nil {
		return "", 0, err
	}
	unlock := a.lockWorkspacePath(resolvedPath)
	defer unlock()

	data := []byte(content)
	if int64(len(data)) > workspaceMaxFileBytes {
		return "", 0, fmt.Errorf("file exceeds max size (%d bytes)", workspaceMaxFileBytes)
	}

	a.workspaceQuotaMu.Lock()
	defer a.workspaceQuotaMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
		return "", 0, fmt.Errorf("create parent directories: %w", err)
	}

	existingSize, err := workspaceFileSize(resolvedPath)
	if err != nil {
		return "", 0, err
	}
	projectedTotal, err := a.enforceWorkspaceQuota(existingSize, int64(len(data)))
	if err != nil {
		return "", 0, err
	}

	if err := atomicWriteFile(resolvedPath, data, 0o644); err != nil {
		return "", 0, err
	}
	a.workspaceTotalBytes = projectedTotal

	return relPath, int64(len(data)), nil
}

func (a *App) appendWorkspaceFile(path, content string) (relPath string, appendedBytes int64, err error) {
	resolvedPath, relPath, err := a.resolveWorkspacePath(path, false)
	if err != nil {
		return "", 0, err
	}
	unlock := a.lockWorkspacePath(resolvedPath)
	defer unlock()

	appendData := []byte(content)
	if !strings.HasSuffix(content, "\n") {
		appendData = append(appendData, '\n')
	}

	a.workspaceQuotaMu.Lock()
	defer a.workspaceQuotaMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
		return "", 0, fmt.Errorf("create parent directories: %w", err)
	}

	existingSize, err := workspaceFileSize(resolvedPath)
	if err != nil {
		return "", 0, err
	}
	finalSize := existingSize + int64(len(appendData))
	if finalSize > workspaceMaxFileBytes {
		return "", 0, fmt.Errorf("file exceeds max size (%d bytes)", workspaceMaxFileBytes)
	}
	projectedTotal, err := a.enforceWorkspaceQuota(existingSize, finalSize)
	if err != nil {
		return "", 0, err
	}

	f, err := os.OpenFile(resolvedPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	if _, err := f.Write(appendData); err != nil {
		return "", 0, err
	}
	if err := f.Sync(); err != nil {
		return "", 0, err
	}
	a.workspaceTotalBytes = projectedTotal

	return relPath, int64(len(appendData)), nil
}

func (a *App) listWorkspaceDir(path string) (string, error) {
	resolvedPath, relPath, err := a.resolveWorkspacePath(path, true)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path %q is not a directory", relPath)
	}

	entries, err := os.ReadDir(resolvedPath)
	if err != nil {
		return "", err
	}

	out := listDirResult{
		Path:    relPath,
		Entries: make([]listDirEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		item := listDirEntry{Name: entry.Name()}
		entryInfo, err := entry.Info()
		if err != nil {
			return "", err
		}
		switch {
		case entryInfo.IsDir():
			item.Type = "dir"
		case entryInfo.Mode().IsRegular():
			item.Type = "file"
			item.SizeBytes = entryInfo.Size()
		default:
			item.Type = "other"
		}
		out.Entries = append(out.Entries, item)
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (a *App) enforceWorkspaceQuota(currentFileBytes, newFileBytes int64) (int64, error) {
	if newFileBytes > workspaceMaxFileBytes {
		return 0, fmt.Errorf("file exceeds max size (%d bytes)", workspaceMaxFileBytes)
	}
	projectedTotal := a.workspaceTotalBytes - currentFileBytes + newFileBytes
	if projectedTotal < 0 {
		projectedTotal = 0
	}
	if projectedTotal > workspaceMaxTotalBytes {
		return 0, fmt.Errorf("workspace exceeds max size (%d bytes)", workspaceMaxTotalBytes)
	}
	return projectedTotal, nil
}

func workspaceFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	if info.IsDir() {
		return 0, fmt.Errorf("path %q is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("path %q is not a regular file", path)
	}
	return info.Size(), nil
}

func workspaceTotalSizeBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func atomicWriteFile(path string, data []byte, perm fs.FileMode) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(path), ".pincer-tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Chmod(perm); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}

	if dirHandle, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}

	cleanup = false
	return nil
}
