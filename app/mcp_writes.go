package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	appmcp "github.com/mslxl/natcatty/application/mcp"
	appsession "github.com/mslxl/natcatty/application/session"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// mcpWriteExecutor maps the catalog's write tools onto application services.
// The policy handler calls it only after Authorize has enforced scope, mode,
// ownership, and approval. Every call gets a deadline and every shell command
// passes the blocklist before it reaches a Session.

// mcpHostOpener opens a real Host Session and attaches it to a Tab. The app
// supplies a closure over the shell state; tests can supply a fake.
type mcpHostOpener func(ctx context.Context, host domainvault.Host) (model.SessionID, error)

type mcpWriteExecutor struct {
	policy     *appmcp.Service
	sessions   *appsession.Service
	vault      *appvault.Service
	forwarding *appforwarding.Manager
	transfer   *apptransfer.Service
	jobs       *mcpJobManager
	openHost   mcpHostOpener

	toolTimeout time.Duration
}

func newMCPWriteExecutor(policy *appmcp.Service, sessions *appsession.Service,
	vault *appvault.Service, forwarding *appforwarding.Manager,
	transfer *apptransfer.Service, opener mcpHostOpener) *mcpWriteExecutor {
	return &mcpWriteExecutor{
		policy:      policy,
		sessions:    sessions,
		vault:       vault,
		forwarding:  forwarding,
		transfer:    transfer,
		jobs:        newMCPJobManager(sessions),
		openHost:    opener,
		toolTimeout: appmcp.DefaultToolTimeout,
	}
}

// Execute runs one authorized write. The handler has already checked policy;
// this method enforces the per-call deadline and the command blocklist.
func (e *mcpWriteExecutor) Execute(ctx context.Context, toolName string,
	arguments map[string]any, decision appmcp.Decision) map[string]any {

	// Every write, including Vault and forwarding calls, gets the same
	// per-call deadline as a shell command. A shell command already carries
	// its deadline in CommandOptions; wrapping it in an earlier context timer
	// would turn the typed timeout into a cancellation, so those paths use
	// the caller context and their own timer.
	callCtx := ctx
	cancel := func() {}
	switch toolName {
	case "terminal_execute", "terminal_start", "snippets_run":
	default:
		callCtx, cancel = e.bounded(ctx)
	}
	defer cancel()
	result, err := e.execute(callCtx, toolName, arguments, decision)
	if err != nil {
		e.recordFailure(toolName, err)
		return mcpPolicyFailure(err, toolName)
	}
	e.recordSuccess(toolName, result)
	return result
}

func (e *mcpWriteExecutor) execute(ctx context.Context, toolName string,
	arguments map[string]any, decision appmcp.Decision) (map[string]any, error) {

	switch toolName {
	case "scripts_create", "scripts_update", "scripts_delete", "scripts_run",
		"scripts_run_stop", "scripts_run_pause", "scripts_run_resume",
		"scripts_targets_set", "host_connect_scripts_set":
		return nil, appmcp.ToolUnavailableError(toolName,
			"Script execution is not available in this build.")
	case "terminal_execute":
		return e.executeCommand(ctx, toolName, arguments)
	case "terminal_start":
		return e.startJob(ctx, toolName, arguments)
	case "sftp_write_file", "sftp_download", "sftp_upload", "sftp_mkdir",
		"sftp_delete", "sftp_rename", "sftp_chmod":
		return e.executeSFTP(ctx, toolName, arguments)
	case "vault_hosts_create", "vault_hosts_update", "vault_hosts_delete",
		"vault_hosts_import", "host_notes_set":
		return e.executeHosts(ctx, toolName, arguments)
	case "vault_notes_create", "vault_notes_update", "vault_notes_delete", "vault_notes_import":
		return e.executeNotes(ctx, toolName, arguments)
	case "vault_groups_create", "vault_groups_update", "vault_groups_delete":
		return e.executeGroups(ctx, toolName, arguments)
	case "snippets_run", "snippets_create", "snippets_update", "snippets_delete":
		return e.executeSnippets(ctx, toolName, arguments)
	case "portforward_rules_create", "portforward_rules_update",
		"portforward_rules_duplicate", "portforward_rules_delete",
		"portforward_start", "portforward_stop":
		return e.executeForwarding(ctx, toolName, arguments)
	default:
		return nil, appmcp.ToolUnavailableError(toolName,
			"External MCP write execution is not available for this tool in this build.")
	}
}

func (e *mcpWriteExecutor) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	if e.toolTimeout <= 0 {
		e.toolTimeout = appmcp.DefaultToolTimeout
	}
	return context.WithTimeout(ctx, e.toolTimeout)
}

func (e *mcpWriteExecutor) recordSuccess(tool string, result map[string]any) {
	status := appmcp.ActivityOK
	rule := ""
	// A timeout is a typed result rather than a Go error so the partial output
	// can travel with it. It must still reach the activity strip as a timeout,
	// not as a successful command with ok:false.
	if code, _ := result["code"].(string); code != "" {
		status = appmcp.ActivityFailed
		switch appmcp.ErrorCode(code) {
		case appmcp.CodeCommandBlocked:
			status = appmcp.ActivityBlocked
		case appmcp.CodeCommandTimeout:
			status = appmcp.ActivityTimeout
		case appmcp.CodeToolUnavailable:
			status = appmcp.ActivityUnavailable
		}
		rule, _ = result["rule"].(string)
	} else {
		if truncated, _ := result["outputTruncated"].(bool); truncated {
			status = appmcp.ActivityTruncated
		}
		if exitCode, ok := result["exitCode"].(int); ok && exitCode != 0 {
			status = appmcp.ActivityFailed
		}
	}
	e.policy.RecordActivity(appmcp.ActivityView{Tool: tool, Status: status, Rule: rule})
}

func (e *mcpWriteExecutor) recordFailure(tool string, err error) {
	status := appmcp.ActivityFailed
	rule := ""
	var typed *appmcp.Error
	if errors.As(err, &typed) {
		rule = typed.Rule
		switch typed.Code {
		case appmcp.CodeCommandBlocked:
			status = appmcp.ActivityBlocked
		case appmcp.CodeCommandTimeout:
			status = appmcp.ActivityTimeout
		case appmcp.CodeToolUnavailable:
			status = appmcp.ActivityUnavailable
		}
	}
	e.policy.RecordActivity(appmcp.ActivityView{Tool: tool, Status: status, Rule: rule})
}

// checkCommand applies the default blocklist before any shell execution. Raw
// serial and network-device Sessions intentionally skip it, matching the
// original bridge.
func (e *mcpWriteExecutor) checkCommand(tool, sessionID, command string) error {
	shell, ok := e.sessions.ShellKind(model.SessionID(sessionID))
	if !ok {
		return appmcp.InvalidArgumentError(tool, fmt.Sprintf("Session %q was not found.", sessionID))
	}
	if shell == "raw" {
		return nil
	}
	rule, blocked := appmcp.CheckCommandBlocklist(command, appmcp.ShellKind(shell))
	if blocked {
		return appmcp.CommandBlockedError(tool, rule)
	}
	return nil
}

func (e *mcpWriteExecutor) executeCommand(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	sessionID := stringParam(arguments, "sessionId")
	if sessionID == "" {
		return nil, appmcp.InvalidArgumentError(tool, "sessionId is required.")
	}
	command := stringParam(arguments, "command")
	if strings.TrimSpace(command) == "" {
		return nil, appmcp.InvalidArgumentError(tool, "command is required.")
	}
	if err := e.checkCommand(tool, sessionID, command); err != nil {
		return nil, err
	}
	// Serial and network-device Sessions are raw byte streams; the original
	// bridge keeps a smaller 512 KiB window for them than for a shell.
	outputLimit := appmcp.ForegroundOutputLimit
	if shell, ok := e.sessions.ShellKind(model.SessionID(sessionID)); ok && shell == "raw" {
		outputLimit = appmcp.RawOutputLimit
	}
	result, err := e.sessions.Command(ctx, model.SessionID(sessionID), command,
		appsession.CommandOptions{
			Timeout:        e.toolTimeout,
			MaxOutputBytes: outputLimit,
		})
	if err != nil {
		return e.commandErrorResult(tool, result, err)
	}
	response := map[string]any{
		"ok":               true,
		"stdout":           result.Stdout,
		"stderr":           "",
		"exitCode":         result.ExitCode,
		"hasExitCode":      result.HasExitCode,
		"outputTruncated":  result.Truncated,
		"outputBaseOffset": result.OutputBaseOffset,
		"totalOutputChars": result.TotalOutputChars,
		"outputMode":       result.OutputMode,
		"noResponse":       result.NoResponse,
	}
	if result.HasExitCode && result.ExitCode != 0 {
		response["ok"] = false
		response["error"] = fmt.Sprintf("Command exited with code %d.", result.ExitCode)
	}
	return response, nil
}

func (e *mcpWriteExecutor) commandErrorResult(tool string, result appsession.CommandResult,
	err error) (map[string]any, error) {

	switch {
	case errors.Is(err, appsession.ErrCommandTimeout):
		typed := appmcp.Result(appmcp.CommandTimeoutError(tool, e.toolTimeout))
		typed["timedOut"] = true
		typed["stdout"] = result.Stdout
		typed["outputTruncated"] = result.Truncated
		typed["outputBaseOffset"] = result.OutputBaseOffset
		return typed, nil
	case errors.Is(err, appsession.ErrCommandCancelled):
		typed := appmcp.Result(appmcp.CommandTimeoutError(tool, e.toolTimeout))
		typed["error"] = "Command cancelled."
		typed["code"] = string(appmcp.CodeCommandTimeout)
		typed["timedOut"] = true
		typed["stdout"] = result.Stdout
		return typed, nil
	case errors.Is(err, appsession.ErrCommandBusy):
		return nil, appmcp.InvalidArgumentError(tool,
			"Session already has another command in progress. Wait for it to finish.")
	case errors.Is(err, appsession.ErrCommandClosed):
		return nil, appmcp.InvalidArgumentError(tool, "Session is closed.")
	default:
		return nil, appmcp.ToolUnavailableError(tool, err.Error())
	}
}

func (e *mcpWriteExecutor) startJob(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	sessionID := stringParam(arguments, "sessionId")
	if sessionID == "" {
		return nil, appmcp.InvalidArgumentError(tool, "sessionId is required.")
	}
	command := stringParam(arguments, "command")
	if strings.TrimSpace(command) == "" {
		return nil, appmcp.InvalidArgumentError(tool, "command is required.")
	}
	shell, ok := e.sessions.ShellKind(model.SessionID(sessionID))
	if !ok {
		return nil, appmcp.InvalidArgumentError(tool, fmt.Sprintf("Session %q was not found.", sessionID))
	}
	if shell == "raw" {
		return nil, appmcp.ToolUnavailableError(tool,
			"Background execution currently supports shell-backed PTY sessions only.")
	}
	if err := e.checkCommand(tool, sessionID, command); err != nil {
		return nil, err
	}
	return e.jobs.Start(ctx, model.SessionID(sessionID), command)
}

// StopJob implements terminal_stop after the handler's ownership checks.
func (e *mcpWriteExecutor) StopJob(jobID string) map[string]any {
	if jobID == "" {
		return mcpFailure("jobId is required.")
	}
	result, err := e.jobs.Stop(jobID)
	if err != nil {
		return mcpFailure("Background job not found.")
	}
	return result
}

// PollJob implements terminal_poll for the read bridge.
func (e *mcpWriteExecutor) PollJob(jobID string, offset int) map[string]any {
	result, err := e.jobs.Poll(jobID, offset)
	if err != nil {
		return mcpFailure("Background job not found.")
	}
	return result
}

func (e *mcpWriteExecutor) executeSFTP(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	sessionID := stringParam(arguments, "sessionId")
	if sessionID == "" {
		return nil, appmcp.InvalidArgumentError(tool, "sessionId is required.")
	}
	callCtx, cancel := e.bounded(ctx)
	defer cancel()
	files, err := e.openRemoteFiles(callCtx, sessionID)
	if err != nil {
		return nil, e.sftpError(tool, err)
	}
	defer files.Close()

	switch tool {
	case "sftp_write_file":
		path := stringParam(arguments, "path")
		if path == "" {
			return nil, appmcp.InvalidArgumentError(tool, "path is required.")
		}
		content := stringParam(arguments, "content")
		if err := e.transfer.WriteFile(callCtx, files, path, []byte(content)); err != nil {
			return nil, e.sftpError(tool, err)
		}
		return map[string]any{"ok": true, "path": path}, nil

	case "sftp_mkdir":
		path := stringParam(arguments, "path")
		if path == "" {
			return nil, appmcp.InvalidArgumentError(tool, "path is required.")
		}
		if err := files.MkdirAll(callCtx, path); err != nil {
			return nil, e.sftpError(tool, err)
		}
		return map[string]any{"ok": true, "path": path}, nil

	case "sftp_delete":
		path := stringParam(arguments, "path")
		if path == "" {
			return nil, appmcp.InvalidArgumentError(tool, "path is required.")
		}
		info, statErr := files.Stat(callCtx, path)
		switch {
		case statErr != nil:
			return nil, e.sftpError(tool, statErr)
		case info.IsDir():
			err = files.RemoveAll(callCtx, path)
		default:
			err = files.Remove(callCtx, path)
		}
		if err != nil {
			return nil, e.sftpError(tool, err)
		}
		return map[string]any{"ok": true, "path": path}, nil

	case "sftp_rename":
		oldPath := stringParam(arguments, "oldPath")
		newPath := stringParam(arguments, "newPath")
		if oldPath == "" || newPath == "" {
			return nil, appmcp.InvalidArgumentError(tool, "oldPath and newPath are required.")
		}
		if err := files.Rename(callCtx, oldPath, newPath); err != nil {
			return nil, e.sftpError(tool, err)
		}
		return map[string]any{"ok": true, "oldPath": oldPath, "newPath": newPath}, nil

	case "sftp_chmod":
		path := stringParam(arguments, "path")
		modeText := stringParam(arguments, "mode")
		if path == "" || modeText == "" {
			return nil, appmcp.InvalidArgumentError(tool, "path and mode are required.")
		}
		mode, parseErr := strconv.ParseUint(strings.TrimSpace(modeText), 8, 32)
		if parseErr != nil {
			return nil, appmcp.InvalidArgumentError(tool, "mode must be an octal permission value.")
		}
		if err := files.Chmod(callCtx, path, uint32(mode)); err != nil {
			return nil, e.sftpError(tool, err)
		}
		return map[string]any{"ok": true, "path": path, "mode": modeText}, nil

	case "sftp_download":
		remotePath := stringParam(arguments, "remotePath")
		localPath := stringParam(arguments, "localPath")
		if remotePath == "" || localPath == "" {
			return nil, appmcp.InvalidArgumentError(tool, "remotePath and localPath are required.")
		}
		return e.transferBetween(callCtx, tool, files, localPath, remotePath, true)

	case "sftp_upload":
		localPath := stringParam(arguments, "localPath")
		remotePath := stringParam(arguments, "remotePath")
		if remotePath == "" || localPath == "" {
			return nil, appmcp.InvalidArgumentError(tool, "localPath and remotePath are required.")
		}
		return e.transferBetween(callCtx, tool, files, localPath, remotePath, false)
	}
	return nil, appmcp.ToolUnavailableError(tool, "Unsupported SFTP operation.")
}

func (e *mcpWriteExecutor) openRemoteFiles(ctx context.Context,
	sessionID string) (apptransfer.FileSystem, error) {

	description, ok := e.sessions.Describe(model.SessionID(sessionID))
	if !ok {
		return nil, fmt.Errorf("Session %q was not found.", sessionID)
	}
	if description.HostID == "" {
		return nil, errors.New("SFTP requires a Session with a saved Host.")
	}
	if e.transfer == nil {
		return nil, errors.New("SFTP is not available in this build.")
	}
	return e.transfer.OpenRemote(ctx, description.HostID, description.Label)
}

func (e *mcpWriteExecutor) transferBetween(ctx context.Context, tool string,
	remote apptransfer.FileSystem, localPath, remotePath string, download bool) (map[string]any, error) {

	local, err := e.transfer.OpenLocal(ctx)
	if err != nil {
		return nil, e.sftpError(tool, err)
	}
	defer local.Close()
	transferID := newMCPTransferID()
	var source, dest apptransfer.FileSystem
	var sourcePath, targetPath string
	var kind = "upload"
	if download {
		source, sourcePath = remote, remotePath
		dest, targetPath = local, localPath
		kind = "download"
	} else {
		source, sourcePath = local, localPath
		dest, targetPath = remote, remotePath
	}
	destDir := dest.Parent(targetPath)
	result, err := e.transfer.Copy(ctx, apptransfer.CopyRequest{
		Source:   source,
		Dest:     dest,
		Sources:  []string{sourcePath},
		DestDir:  destDir,
		Conflict: apptransfer.ConflictReplace,
		Kind:     transferKind(kind),
	})
	if err != nil {
		return nil, e.sftpError(tool, err)
	}
	// Copy keeps the source base name. If the caller asked for a different
	// destination name, rename it in place.
	copied := dest.Join(destDir, dest.Base(sourcePath))
	if copied != targetPath && len(result.Written) > 0 {
		copied = result.Written[0]
	}
	if copied != targetPath {
		_ = dest.Remove(ctx, targetPath)
		if renameErr := dest.Rename(ctx, copied, targetPath); renameErr != nil {
			return nil, e.sftpError(tool, renameErr)
		}
		result.Written = []string{targetPath}
	}
	return map[string]any{
		"ok":         true,
		"transferId": transferID,
		"sourcePath": sourcePath,
		"targetPath": targetPath,
		"bytes":      result.Bytes,
		"written":    result.Written,
	}, nil
}

func transferKind(direction string) domainsftp.OperationKind {
	if direction == "upload" {
		return domainsftp.OpUpload
	}
	return domainsftp.OpDownload
}

func newMCPTransferID() string {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "transfer_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "transfer_" + strconv.FormatInt(time.Now().UnixNano(), 36) + "_" + hex.EncodeToString(random)
}

func (e *mcpWriteExecutor) sftpError(tool string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || apptransfer.KindOf(err) == apptransfer.KindCancelled {
		return appmcp.CommandTimeoutError(tool, e.toolTimeout)
	}
	return appmcp.ToolUnavailableError(tool, err.Error())
}

// OpenHost implements host_open. The default reuses a live scoped Session for
// the Host; newTab or no live Session opens a real Host Session through the
// shell state.
func (e *mcpWriteExecutor) OpenHost(ctx context.Context, arguments map[string]any,
	decision appmcp.Decision) map[string]any {

	hostID := stringParam(arguments, "hostId")
	if hostID == "" {
		return mcpFailure("hostId is required.")
	}
	newTab := boolArg(arguments, "newTab")
	if !newTab {
		for _, description := range e.sessions.DescribeAll() {
			if string(description.HostID) == hostID &&
				appmcp.SessionAllowed(decision.Sessions, string(description.ID)) {
				// Reuse is visibility-only, matching the original bridge. It must
				// not grant ownership: session_close may only close a Session this
				// external client opened through newTab.
				return map[string]any{
					"ok": true, "sessionId": string(description.ID), "hostId": hostID,
					"status": "reused",
				}
			}
		}
	}
	host, err := e.vault.Get(ctx, model.HostID(hostID))
	if err != nil {
		return mcpFailure(fmt.Sprintf("Host %q was not found.", hostID))
	}
	if e.openHost == nil {
		return mcpFailure("Opening a new Host tab is not available in this build.")
	}
	callCtx, cancel := e.bounded(ctx)
	defer cancel()
	sessionID, err := e.openHost(callCtx, host)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return mcpPolicyFailure(appmcp.CommandTimeoutError("host_open", e.toolTimeout), "host_open")
		}
		return mcpFailure(err.Error())
	}
	generation := e.policy.GrantOwnedSession(string(sessionID), hostID)
	return map[string]any{
		"ok": true, "sessionId": string(sessionID), "hostId": hostID,
		"status": "opened", "generation": generation,
	}
}

// Utility argument readers. Catalog inputs are strings in inputShape, but a
// real client may send native JSON booleans/numbers; both are accepted.
func boolArg(arguments map[string]any, key string) bool {
	switch value := arguments[key].(type) {
	case bool:
		return value
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(value))
		return parsed
	case float64:
		return value != 0
	default:
		return false
	}
}

func intArg(arguments map[string]any, key string) (int, bool) {
	switch value := arguments[key].(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func stringSliceArg(arguments map[string]any, key string) []string {
	switch value := arguments[key].(type) {
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	case []string:
		return value
	case string:
		return parseStringList(value)
	default:
		return nil
	}
}

func parseStringList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var decoded []string
	if strings.HasPrefix(value, "[") && json.Unmarshal([]byte(value), &decoded) == nil {
		return decoded
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseJSONObject(value string) (map[string]any, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return map[string]any{}, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func parseJSONArray(value string) ([]map[string]any, error) {
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(value)), &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}
