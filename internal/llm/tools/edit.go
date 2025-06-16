package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cap-ai/cap/internal/config"
	"github.com/cap-ai/cap/internal/diff"
	"github.com/cap-ai/cap/internal/history"
	"github.com/cap-ai/cap/internal/logging"
	"github.com/cap-ai/cap/internal/lsp"
	"github.com/cap-ai/cap/internal/permission"
)

type EditParams struct {
	FilePath  string `json:"file_path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

type EditPermissionsParams struct {
	FilePath string `json:"file_path"`
	Diff     string `json:"diff"`
}

type EditResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
}

type editTool struct {
	lspClients  map[string]*lsp.Client
	permissions permission.Service
	files       history.Service
}

const (
	EditToolName    = "edit"
	editDescription = `Edits files by replacing text, creating new files, or deleting content. For moving or renaming files, use the Bash tool with the 'mv' command instead. For larger file edits, use the FileWrite tool to overwrite files.

Before using this tool:

1. Use the FileRead tool to understand the file's contents and context

2. Verify the directory path is correct (only applicable when creating new files):
   - Use the LS tool to verify the parent directory exists and is the correct location

To make a file edit, provide the following:
1. file_path: The absolute path to the file to modify (must be absolute, not relative)
2. old_string: The text to replace (must be unique within the file, and must match the file contents exactly, including all whitespace and indentation)
3. new_string: The edited text to replace the old_string

Special cases:
- To create a new file: provide file_path and new_string, leave old_string empty
- To delete content: provide file_path and old_string, leave new_string empty

The tool will replace ONE occurrence of old_string with new_string in the specified file.

CRITICAL REQUIREMENTS FOR USING THIS TOOL:

1. UNIQUENESS: The old_string MUST uniquely identify the specific instance you want to change. This means:
   - Include AT LEAST 3-5 lines of context BEFORE the change point
   - Include AT LEAST 3-5 lines of context AFTER the change point
   - Include all whitespace, indentation, and surrounding code exactly as it appears in the file

2. SINGLE INSTANCE: This tool can only change ONE instance at a time. If you need to change multiple instances:
   - Make separate calls to this tool for each instance
   - Each call must uniquely identify its specific instance using extensive context

3. VERIFICATION: Before using this tool:
   - Check how many instances of the target text exist in the file
   - If multiple instances exist, gather enough context to uniquely identify each one
   - Plan separate tool calls for each instance

WARNING: If you do not follow these requirements:
   - The tool will fail if old_string matches multiple locations
   - The tool will fail if old_string doesn't match exactly (including whitespace)
   - You may change the wrong instance if you don't include enough context

When making edits:
   - Ensure the edit results in idiomatic, correct code
   - Do not leave the code in a broken state
   - Always use absolute file paths (starting with /)

Remember: when making multiple file edits in a row to the same file, you should prefer to send all edits in a single message with multiple calls to this tool, rather than multiple messages with a single call each.`
)

func NewEditTool(lspClients map[string]*lsp.Client, permissions permission.Service, files history.Service) BaseTool {
	return &editTool{
		lspClients:  lspClients,
		permissions: permissions,
		files:       files,
	}
}

func (e *editTool) Info() ToolInfo {
	return ToolInfo{
		Name:        EditToolName,
		Description: editDescription,
		Parameters: map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The absolute path to the file to modify",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "The text to replace",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "The text to replace it with",
			},
		},
		Required: []string{"file_path", "old_string", "new_string"},
	}
}

func (e *editTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params EditParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("invalid parameters"), nil
	}

	if params.FilePath == "" {
		return NewTextErrorResponse("file_path is required"), nil
	}

	// 2025.06.15 Kawata updated replace-delete-logic
	params.OldString = cleanOldNewString(params.OldString)
	params.NewString = cleanOldNewString(params.NewString)

	// 2025.06.15 remove the check for absolute path
	// if !filepath.IsAbs(params.FilePath) {
	// 	wd := config.WorkingDirectory()
	// 	params.FilePath = filepath.Join(wd, params.FilePath)
	// }
	wd := config.WorkingDirectory()
	params.FilePath = filepath.Join(wd, params.FilePath)

	var response ToolResponse
	var err error

	if params.OldString == "" {
		response, err = e.createNewFile(ctx, params.FilePath, params.NewString)
		if err != nil {
			return response, err
		}
	}

	if params.NewString == "" {
		response, err = e.deleteContent(ctx, params.FilePath, params.OldString)
		if err != nil {
			return response, err
		}
	}

	response, err = e.replaceContent(ctx, params.FilePath, params.OldString, params.NewString)
	if err != nil {
		return response, err
	}
	if response.IsError {
		// Return early if there was an error during content replacement
		// This prevents unnecessary LSP diagnostics processing
		return response, nil
	}

	waitForLspDiagnostics(ctx, params.FilePath, e.lspClients)
	text := fmt.Sprintf("<result>\n%s\n</result>\n", response.Content)
	text += getDiagnostics(params.FilePath, e.lspClients)
	response.Content = text
	return response, nil
}

func (e *editTool) createNewFile(ctx context.Context, filePath, content string) (ToolResponse, error) {
	fileInfo, err := os.Stat(filePath)
	if err == nil {
		if fileInfo.IsDir() {
			return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("file already exists: %s", filePath)), nil
	} else if !os.IsNotExist(err) {
		return ToolResponse{}, fmt.Errorf("failed to access file: %w", err)
	}

	dir := filepath.Dir(filePath)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return ToolResponse{}, fmt.Errorf("failed to create parent directories: %w", err)
	}

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return ToolResponse{}, fmt.Errorf("session ID and message ID are required for creating a new file")
	}

	diff, additions, removals := diff.GenerateDiff(
		"",
		content,
		filePath,
	)
	rootDir := config.WorkingDirectory()
	permissionPath := filepath.Dir(filePath)
	if strings.HasPrefix(filePath, rootDir) {
		permissionPath = rootDir
	}
	p := e.permissions.Request(
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        permissionPath,
			ToolName:    EditToolName,
			Action:      "write",
			Description: fmt.Sprintf("Create file %s", filePath),
			Params: EditPermissionsParams{
				FilePath: filePath,
				Diff:     diff,
			},
		},
	)
	if !p {
		return ToolResponse{}, permission.ErrorPermissionDenied
	}

	err = os.WriteFile(filePath, []byte(content), 0o644)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
	}

	// File can't be in the history so we create a new file history
	_, err = e.files.Create(ctx, sessionID, filePath, "")
	if err != nil {
		// Log error but don't fail the operation
		return ToolResponse{}, fmt.Errorf("error creating file history: %w", err)
	}

	// Add the new content to the file history
	_, err = e.files.CreateVersion(ctx, sessionID, filePath, content)
	if err != nil {
		// Log error but don't fail the operation
		logging.Debug("Error creating file history version", "error", err)
	}

	recordFileWrite(filePath)
	recordFileRead(filePath)

	return WithResponseMetadata(
		NewTextResponse("File created: "+filePath),
		EditResponseMetadata{
			Diff:      diff,
			Additions: additions,
			Removals:  removals,
		},
	), nil
}

// 2025.06.15 Kawata updated replace-delete-logic
// func (e *editTool) deleteContent(ctx context.Context, filePath, oldString string) (ToolResponse, error) {
// 	fileInfo, err := os.Stat(filePath)
// 	if err != nil {
// 		if os.IsNotExist(err) {
// 			return NewTextErrorResponse(fmt.Sprintf("file not found: %s", filePath)), nil
// 		}
// 		return ToolResponse{}, fmt.Errorf("failed to access file: %w", err)
// 	}

// 	if fileInfo.IsDir() {
// 		return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
// 	}

// 	if getLastReadTime(filePath).IsZero() {
// 		return NewTextErrorResponse("you must read the file before editing it. Use the View tool first"), nil
// 	}

// 	modTime := fileInfo.ModTime()
// 	lastRead := getLastReadTime(filePath)
// 	if modTime.After(lastRead) {
// 		return NewTextErrorResponse(
// 			fmt.Sprintf("file %s has been modified since it was last read (mod time: %s, last read: %s)",
// 				filePath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339),
// 			)), nil
// 	}

// 	content, err := os.ReadFile(filePath)
// 	if err != nil {
// 		return ToolResponse{}, fmt.Errorf("failed to read file: %w", err)
// 	}

// 	oldContent := string(content)

// 	index := strings.Index(oldContent, oldString)
// 	if index == -1 {
// 		return NewTextErrorResponse("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks"), nil
// 	}

// 	lastIndex := strings.LastIndex(oldContent, oldString)
// 	if index != lastIndex {
// 		return NewTextErrorResponse("old_string appears multiple times in the file. Please provide more context to ensure a unique match"), nil
// 	}

// 	newContent := oldContent[:index] + oldContent[index+len(oldString):]

// 	sessionID, messageID := GetContextValues(ctx)

// 	if sessionID == "" || messageID == "" {
// 		return ToolResponse{}, fmt.Errorf("session ID and message ID are required for creating a new file")
// 	}

// 	diff, additions, removals := diff.GenerateDiff(
// 		oldContent,
// 		newContent,
// 		filePath,
// 	)

// 	rootDir := config.WorkingDirectory()
// 	permissionPath := filepath.Dir(filePath)
// 	if strings.HasPrefix(filePath, rootDir) {
// 		permissionPath = rootDir
// 	}
// 	p := e.permissions.Request(
// 		permission.CreatePermissionRequest{
// 			SessionID:   sessionID,
// 			Path:        permissionPath,
// 			ToolName:    EditToolName,
// 			Action:      "write",
// 			Description: fmt.Sprintf("Delete content from file %s", filePath),
// 			Params: EditPermissionsParams{
// 				FilePath: filePath,
// 				Diff:     diff,
// 			},
// 		},
// 	)
// 	if !p {
// 		return ToolResponse{}, permission.ErrorPermissionDenied
// 	}

// 	err = os.WriteFile(filePath, []byte(newContent), 0o644)
// 	if err != nil {
// 		return ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
// 	}

// 	// Check if file exists in history
// 	file, err := e.files.GetByPathAndSession(ctx, filePath, sessionID)
// 	if err != nil {
// 		_, err = e.files.Create(ctx, sessionID, filePath, oldContent)
// 		if err != nil {
// 			// Log error but don't fail the operation
// 			return ToolResponse{}, fmt.Errorf("error creating file history: %w", err)
// 		}
// 	}
// 	if file.Content != oldContent {
// 		// User Manually changed the content store an intermediate version
// 		_, err = e.files.CreateVersion(ctx, sessionID, filePath, oldContent)
// 		if err != nil {
// 			logging.Debug("Error creating file history version", "error", err)
// 		}
// 	}
// 	// Store the new version
// 	_, err = e.files.CreateVersion(ctx, sessionID, filePath, "")
// 	if err != nil {
// 		logging.Debug("Error creating file history version", "error", err)
// 	}

// 	recordFileWrite(filePath)
// 	recordFileRead(filePath)

// 	return WithResponseMetadata(
// 		NewTextResponse("Content deleted from file: "+filePath),
// 		EditResponseMetadata{
// 			Diff:      diff,
// 			Additions: additions,
// 			Removals:  removals,
// 		},
// 	), nil
// }

// 2025.06.15 Kawata updated replace-delete-logic
// func (e *editTool) replaceContent(ctx context.Context, filePath, oldString, newString string) (ToolResponse, error) {
// 	fileInfo, err := os.Stat(filePath)
// 	if err != nil {
// 		if os.IsNotExist(err) {
// 			return NewTextErrorResponse(fmt.Sprintf("file not found: %s", filePath)), nil
// 		}
// 		return ToolResponse{}, fmt.Errorf("failed to access file: %w", err)
// 	}

// 	if fileInfo.IsDir() {
// 		return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
// 	}

// 	if getLastReadTime(filePath).IsZero() {
// 		return NewTextErrorResponse("you must read the file before editing it. Use the View tool first"), nil
// 	}

// 	modTime := fileInfo.ModTime()
// 	lastRead := getLastReadTime(filePath)
// 	if modTime.After(lastRead) {
// 		return NewTextErrorResponse(
// 			fmt.Sprintf("file %s has been modified since it was last read (mod time: %s, last read: %s)",
// 				filePath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339),
// 			)), nil
// 	}

// 	content, err := os.ReadFile(filePath)
// 	if err != nil {
// 		return ToolResponse{}, fmt.Errorf("failed to read file: %w", err)
// 	}

// 	oldContent := string(content)

// 	index := strings.Index(oldContent, oldString)
// 	if index == -1 {
// 		return NewTextErrorResponse("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks"), nil
// 	}

// 	lastIndex := strings.LastIndex(oldContent, oldString)
// 	if index != lastIndex {
// 		return NewTextErrorResponse("old_string appears multiple times in the file. Please provide more context to ensure a unique match"), nil
// 	}

// 	newContent := oldContent[:index] + newString + oldContent[index+len(oldString):]

// 	if oldContent == newContent {
// 		return NewTextErrorResponse("new content is the same as old content. No changes made."), nil
// 	}
// 	sessionID, messageID := GetContextValues(ctx)

// 	if sessionID == "" || messageID == "" {
// 		return ToolResponse{}, fmt.Errorf("session ID and message ID are required for creating a new file")
// 	}
// 	diff, additions, removals := diff.GenerateDiff(
// 		oldContent,
// 		newContent,
// 		filePath,
// 	)
// 	rootDir := config.WorkingDirectory()
// 	permissionPath := filepath.Dir(filePath)
// 	if strings.HasPrefix(filePath, rootDir) {
// 		permissionPath = rootDir
// 	}
// 	p := e.permissions.Request(
// 		permission.CreatePermissionRequest{
// 			SessionID:   sessionID,
// 			Path:        permissionPath,
// 			ToolName:    EditToolName,
// 			Action:      "write",
// 			Description: fmt.Sprintf("Replace content in file %s", filePath),
// 			Params: EditPermissionsParams{
// 				FilePath: filePath,
// 				Diff:     diff,
// 			},
// 		},
// 	)
// 	if !p {
// 		return ToolResponse{}, permission.ErrorPermissionDenied
// 	}

// 	err = os.WriteFile(filePath, []byte(newContent), 0o644)
// 	if err != nil {
// 		return ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
// 	}

// 	// Check if file exists in history
// 	file, err := e.files.GetByPathAndSession(ctx, filePath, sessionID)
// 	if err != nil {
// 		_, err = e.files.Create(ctx, sessionID, filePath, oldContent)
// 		if err != nil {
// 			// Log error but don't fail the operation
// 			return ToolResponse{}, fmt.Errorf("error creating file history: %w", err)
// 		}
// 	}
// 	if file.Content != oldContent {
// 		// User Manually changed the content store an intermediate version
// 		_, err = e.files.CreateVersion(ctx, sessionID, filePath, oldContent)
// 		if err != nil {
// 			logging.Debug("Error creating file history version", "error", err)
// 		}
// 	}
// 	// Store the new version
// 	_, err = e.files.CreateVersion(ctx, sessionID, filePath, newContent)
// 	if err != nil {
// 		logging.Debug("Error creating file history version", "error", err)
// 	}

// 	recordFileWrite(filePath)
// 	recordFileRead(filePath)

// 	return WithResponseMetadata(
// 		NewTextResponse("Content replaced in file: "+filePath),
// 		EditResponseMetadata{
// 			Diff:      diff,
// 			Additions: additions,
// 			Removals:  removals,
// 		}), nil
// }

// 2025.06.15 Kawata updated replace-delete-logic
func (e *editTool) deleteContent(ctx context.Context, filePath, oldString string) (ToolResponse, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewTextErrorResponse(fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return ToolResponse{}, fmt.Errorf("failed to access file: %w", err)
	}

	if fileInfo.IsDir() {
		return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
	}

	if getLastReadTime(filePath).IsZero() {
		return NewTextErrorResponse("you must read the file before editing it. Use the View tool first"), nil
	}

	modTime := fileInfo.ModTime()
	lastRead := getLastReadTime(filePath)
	if modTime.After(lastRead) {
		return NewTextErrorResponse(
			fmt.Sprintf("file %s has been modified since it was last read (mod time: %s, last read: %s)",
				filePath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339),
			)), nil
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to read file: %w", err)
	}

	oldContent := string(content)

	// 空白文字を無視してマッチングを行う
	start, end, found := findMatchIgnoringWhitespace(oldContent, oldString)
	if !found {
		return NewTextErrorResponse("old_string not found in file when ignoring whitespace differences"), nil
	}

	newContent := oldContent[:start] + oldContent[end:]

	sessionID, messageID := GetContextValues(ctx)

	if sessionID == "" || messageID == "" {
		return ToolResponse{}, fmt.Errorf("session ID and message ID are required for creating a new file")
	}

	diff, additions, removals := diff.GenerateDiff(
		oldContent,
		newContent,
		filePath,
	)

	rootDir := config.WorkingDirectory()
	permissionPath := filepath.Dir(filePath)
	if strings.HasPrefix(filePath, rootDir) {
		permissionPath = rootDir
	}
	p := e.permissions.Request(
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        permissionPath,
			ToolName:    EditToolName,
			Action:      "write",
			Description: fmt.Sprintf("Delete content from file %s", filePath),
			Params: EditPermissionsParams{
				FilePath: filePath,
				Diff:     diff,
			},
		},
	)
	if !p {
		return ToolResponse{}, permission.ErrorPermissionDenied
	}

	err = os.WriteFile(filePath, []byte(newContent), 0o644)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
	}

	// Check if file exists in history
	file, err := e.files.GetByPathAndSession(ctx, filePath, sessionID)
	if err != nil {
		_, err = e.files.Create(ctx, sessionID, filePath, oldContent)
		if err != nil {
			// Log error but don't fail the operation
			return ToolResponse{}, fmt.Errorf("error creating file history: %w", err)
		}
	}
	if file.Content != oldContent {
		// User Manually changed the content store an intermediate version
		_, err = e.files.CreateVersion(ctx, sessionID, filePath, oldContent)
		if err != nil {
			logging.Debug("Error creating file history version", "error", err)
		}
	}
	// Store the new version
	_, err = e.files.CreateVersion(ctx, sessionID, filePath, newContent)
	if err != nil {
		logging.Debug("Error creating file history version", "error", err)
	}

	recordFileWrite(filePath)
	recordFileRead(filePath)

	return WithResponseMetadata(
		NewTextResponse("Content deleted from file: "+filePath),
		EditResponseMetadata{
			Diff:      diff,
			Additions: additions,
			Removals:  removals,
		},
	), nil
}

// 2025.06.15 Kawata updated replace-delete-logic
func (e *editTool) replaceContent(ctx context.Context, filePath, oldString, newString string) (ToolResponse, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewTextErrorResponse(fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return ToolResponse{}, fmt.Errorf("failed to access file: %w", err)
	}

	if fileInfo.IsDir() {
		return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
	}

	if getLastReadTime(filePath).IsZero() {
		return NewTextErrorResponse("you must read the file before editing it. Use the View tool first"), nil
	}

	modTime := fileInfo.ModTime()
	lastRead := getLastReadTime(filePath)
	if modTime.After(lastRead) {
		return NewTextErrorResponse(
			fmt.Sprintf("file %s has been modified since it was last read (mod time: %s, last read: %s)",
				filePath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339),
			)), nil
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to read file: %w", err)
	}

	oldContent := string(content)

	// 空白文字を無視してマッチングを行う
	start, end, found := findMatchIgnoringWhitespace(oldContent, oldString)
	if !found {
		return NewTextErrorResponse("old_string not found in file when ignoring whitespace differences"), nil
	}

	newContent := oldContent[:start] + newString + oldContent[end:]

	if oldContent == newContent {
		return NewTextErrorResponse("new content is the same as old content. No changes made."), nil
	}
	sessionID, messageID := GetContextValues(ctx)

	if sessionID == "" || messageID == "" {
		return ToolResponse{}, fmt.Errorf("session ID and message ID are required for creating a new file")
	}
	diff, additions, removals := diff.GenerateDiff(
		oldContent,
		newContent,
		filePath,
	)
	rootDir := config.WorkingDirectory()
	permissionPath := filepath.Dir(filePath)
	if strings.HasPrefix(filePath, rootDir) {
		permissionPath = rootDir
	}
	p := e.permissions.Request(
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        permissionPath,
			ToolName:    EditToolName,
			Action:      "write",
			Description: fmt.Sprintf("Replace content in file %s", filePath),
			Params: EditPermissionsParams{
				FilePath: filePath,
				Diff:     diff,
			},
		},
	)
	if !p {
		return ToolResponse{}, permission.ErrorPermissionDenied
	}

	err = os.WriteFile(filePath, []byte(newContent), 0o644)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
	}

	// Check if file exists in history
	file, err := e.files.GetByPathAndSession(ctx, filePath, sessionID)
	if err != nil {
		_, err = e.files.Create(ctx, sessionID, filePath, oldContent)
		if err != nil {
			// Log error but don't fail the operation
			return ToolResponse{}, fmt.Errorf("error creating file history: %w", err)
		}
	}
	if file.Content != oldContent {
		// User Manually changed the content store an intermediate version
		_, err = e.files.CreateVersion(ctx, sessionID, filePath, oldContent)
		if err != nil {
			logging.Debug("Error creating file history version", "error", err)
		}
	}
	// Store the new version
	_, err = e.files.CreateVersion(ctx, sessionID, filePath, newContent)
	if err != nil {
		logging.Debug("Error creating file history version", "error", err)
	}

	recordFileWrite(filePath)
	recordFileRead(filePath)

	return WithResponseMetadata(
		NewTextResponse("Content replaced in file: "+filePath),
		EditResponseMetadata{
			Diff:      diff,
			Additions: additions,
			Removals:  removals,
		}), nil
}

// 2025.06.15 Kawata updated replace-delete-logic
// oldStringとnewStringから、先頭にあることは相応しくない文字を除去する
func cleanOldNewString(str string) string {
	ngList := []string{"\\", ":", ";"}
	str = strings.TrimSpace(str)
	for _, l := range ngList {
		str = strings.TrimPrefix(str, l)
		str = strings.TrimSpace(str)
	}
	return str
}

func isWhitespaceExceptNewline(r rune) bool {
	return unicode.IsSpace(r) && r != '\n' && r != '\r'
}

// 2025.06.15 Kawata updated replace-delete-logic
// buildIndexMap は文字列から空白文字を除去し、元のインデックスとのマッピングを作成する
func buildIndexMap(content string) (stripped string, indices []int) {
	var sb strings.Builder
	indices = make([]int, 0, utf8.RuneCountInString(content))
	for i, r := range content {
		if !isWhitespaceExceptNewline(r) {
			sb.WriteRune(r)
			indices = append(indices, i)
		}
	}
	return sb.String(), indices
}

// 2025.06.15 Kawata updated replace-delete-logic
// removeWhitespace は文字列からすべての空白文字を除去する
func removeWhitespace(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if !isWhitespaceExceptNewline(r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// ルーン単位でパターンを検索
func findRuneIndex(runes, pattern []rune) int {
	if len(pattern) == 0 {
		return -1
	}
	for i := 0; i <= len(runes)-len(pattern); i++ {
		found := true
		for j := range pattern {
			if runes[i+j] != pattern[j] {
				found = false
				break
			}
		}
		if found {
			return i
		}
	}
	return -1
}

// ルーン単位で最後のパターンを検索
func findLastRuneIndex(runes, pattern []rune) int {
	if len(pattern) == 0 {
		return -1
	}
	for i := len(runes) - len(pattern); i >= 0; i-- {
		found := true
		for j := range pattern {
			if runes[i+j] != pattern[j] {
				found = false
				break
			}
		}
		if found {
			return i
		}
	}
	return -1
}

// 2025.06.15 Kawata updated replace-delete-logic
// findMatchIgnoringWhitespace は改行以外の空白文字を無視してマッチングを行い、元の文字列でのインデックスを返す
func findMatchIgnoringWhitespace(content, pattern string) (start, end int, ok bool) {
	strippedContent, indices := buildIndexMap(content)
	strippedPattern := removeWhitespace(pattern)

	if len(strippedPattern) == 0 {
		return 0, 0, false
	}

	// 整合性チェック
	if utf8.RuneCountInString(strippedContent) != len(indices) {
		fmt.Printf("findMatchIgnoringWhitespace - length mismatch: strippedContent runes=%d, indices=%d\n", utf8.RuneCountInString(strippedContent), len(indices))
		return 0, 0, false
	}

	// ルーンスライスに変換
	runes := []rune(strippedContent)
	patternRunes := []rune(strippedPattern)

	// ルーン単位で検索
	firstRuneIdx := findRuneIndex(runes, patternRunes)
	if firstRuneIdx == -1 {
		return 0, 0, false
	}

	lastRuneIdx := findLastRuneIndex(runes, patternRunes)
	if firstRuneIdx != lastRuneIdx {
		return 0, 0, false
	}

	// 元のインデックス計算
	startIdx := indices[firstRuneIdx]
	endStrippedIdx := firstRuneIdx + len(patternRunes) - 1
	var endIdx int
	if endStrippedIdx < len(indices) {
		endIdx = indices[endStrippedIdx] + 1
	} else {
		endIdx = len(content)
	}

	return startIdx, endIdx, true
}
