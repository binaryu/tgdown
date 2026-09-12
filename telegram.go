package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// User represents a Telegram user
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

// Chat represents a Telegram chat
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// PhotoSize represents one size of a photo or a file / sticker thumbnail
type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Document represents a general file (as opposed to photos or audio)
type Document struct {
	FileID       string     `json:"file_id"`
	FileUniqueID string     `json:"file_unique_id"`
	Thumbnail    *PhotoSize `json:"thumbnail,omitempty"`
	FileName     string     `json:"file_name,omitempty"`
	MimeType     string     `json:"mime_type,omitempty"`
	FileSize     int64      `json:"file_size,omitempty"`
}

// Video represents a video file
type Video struct {
	FileID       string     `json:"file_id"`
	FileUniqueID string     `json:"file_unique_id"`
	Width        int        `json:"width"`
	Height       int        `json:"height"`
	Duration     int        `json:"duration"`
	Thumbnail    *PhotoSize `json:"thumbnail,omitempty"`
	FileName     string     `json:"file_name,omitempty"`
	MimeType     string     `json:"mime_type,omitempty"`
	FileSize     int64      `json:"file_size,omitempty"`
}

// Audio represents an audio file
type Audio struct {
	FileID       string     `json:"file_id"`
	FileUniqueID string     `json:"file_unique_id"`
	Duration     int        `json:"duration"`
	Performer    string     `json:"performer,omitempty"`
	Title        string     `json:"title,omitempty"`
	FileName     string     `json:"file_name,omitempty"`
	MimeType     string     `json:"mime_type,omitempty"`
	FileSize     int64      `json:"file_size,omitempty"`
}

// Voice represents a voice note
type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Message represents a Telegram message
type Message struct {
	MessageID      int64       `json:"message_id"`
	From           *User       `json:"from,omitempty"`
	Chat           *Chat       `json:"chat"`
	Text           string      `json:"text,omitempty"`
	Caption        string      `json:"caption,omitempty"`
	Document       *Document   `json:"document,omitempty"`
	Video          *Video      `json:"video,omitempty"`
	Audio          *Audio      `json:"audio,omitempty"`
	Voice          *Voice      `json:"voice,omitempty"`
	Photo          []PhotoSize `json:"photo,omitempty"`
	ReplyToMessage *Message    `json:"reply_to_message,omitempty"`
	Date           int64       `json:"date"`
}

// TGFile represents Telegram File object returned by getFile
type TGFile struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

// ResponseParameters holds Telegram API error rate-limit hints
type ResponseParameters struct {
	MigrateToChatID int64 `json:"migrate_to_chat_id,omitempty"`
	RetryAfter      int   `json:"retry_after,omitempty"`
}

// APIResponse represents standard Telegram API response envelope
type APIResponse[T any] struct {
	OK          bool                `json:"ok"`
	Result      T                   `json:"result,omitempty"`
	Description string              `json:"description,omitempty"`
	ErrorCode   int                 `json:"error_code,omitempty"`
	Parameters  *ResponseParameters `json:"parameters,omitempty"`
}

// Update represents an incoming update from getUpdates
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message,omitempty"`
}

// TelegramClient manages low-level HTTP interaction with Local Bot API
type TelegramClient struct {
	token      string
	apiBase    string
	httpClient *http.Client
	longPollClient *http.Client
}

// NewTelegramClient creates a new instance with optimized connection pooling
func NewTelegramClient(token, apiBase string) *TelegramClient {
	transport := &http.Transport{
		MaxIdleConns:        10,
		IdleConnTimeout:     60 * time.Second,
		DisableCompression: false,
	}

	return &TelegramClient{
		token:   token,
		apiBase: apiBase,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   15 * time.Minute, // 适配大文件 sendDocument 等待 Local API 回应
		},
		longPollClient: &http.Client{
			Transport: transport,
			Timeout:   75 * time.Second,
		},
	}
}

func (c *TelegramClient) endpoint(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", c.apiBase, c.token, method)
}

// postJSON performs a POST request with JSON payload to Local Bot API
func (c *TelegramClient) postJSON(ctx context.Context, client *http.Client, method string, payload any, result any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP 请求错误: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}

	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("反序列化响应失败 (HTTP %d): %w", resp.StatusCode, err)
	}

	return nil
}

// GetUpdates polls for new updates
func (c *TelegramClient) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	reqData := map[string]any{
		"offset":  offset,
		"timeout": timeoutSec,
		"allowed_updates": []string{"message"},
	}

	var res APIResponse[[]Update]
	if err := c.postJSON(ctx, c.longPollClient, "getUpdates", reqData, &res); err != nil {
		return nil, err
	}

	if !res.OK {
		return nil, fmt.Errorf("TG API 错误 [%d]: %s", res.ErrorCode, res.Description)
	}

	return res.Result, nil
}

// SendMessage sends a plain or formatted text message
func (c *TelegramClient) SendMessage(ctx context.Context, chatID int64, text string, parseMode string) (*Message, error) {
	reqData := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		reqData["parse_mode"] = parseMode
	}

	var res APIResponse[*Message]
	if err := c.postJSON(ctx, c.httpClient, "sendMessage", reqData, &res); err != nil {
		return nil, err
	}

	if !res.OK {
		// 若带有 parse_mode 且遇到实体解析错误，自动降级为无格式纯文本重试
		if parseMode != "" && strings.Contains(strings.ToLower(res.Description), "can't parse entities") {
			delete(reqData, "parse_mode")
			var fallbackRes APIResponse[*Message]
			if fErr := c.postJSON(ctx, c.httpClient, "sendMessage", reqData, &fallbackRes); fErr == nil && fallbackRes.OK {
				return fallbackRes.Result, nil
			}
		}
		return nil, fmt.Errorf("TG API 发送消息失败 [%d]: %s", res.ErrorCode, res.Description)
	}

	return res.Result, nil
}

// EditMessageText edits existing text message, ignoring 'not modified' error
func (c *TelegramClient) EditMessageText(ctx context.Context, chatID int64, messageID int64, text string, parseMode string) (*Message, error) {
	reqData := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}
	if parseMode != "" {
		reqData["parse_mode"] = parseMode
	}

	var res APIResponse[*Message]
	if err := c.postJSON(ctx, c.httpClient, "editMessageText", reqData, &res); err != nil {
		return nil, err
	}

	if !res.OK {
		// 忽略消息内容未变更的错误 (Bad Request: message is not modified)
		if strings.Contains(res.Description, "message is not modified") {
			return nil, nil
		}
		// 若带有 parse_mode 且遇到实体解析错误，自动降级为无格式纯文本重试
		if parseMode != "" && strings.Contains(strings.ToLower(res.Description), "can't parse entities") {
			delete(reqData, "parse_mode")
			var fallbackRes APIResponse[*Message]
			if fErr := c.postJSON(ctx, c.httpClient, "editMessageText", reqData, &fallbackRes); fErr == nil && fallbackRes.OK {
				return fallbackRes.Result, nil
			}
		}
		return nil, fmt.Errorf("TG API 编辑消息失败 [%d]: %s", res.ErrorCode, res.Description)
	}

	return res.Result, nil
}

// SendDocument sends a local file using file:/// protocol via Local Bot API
// 关键优化：严禁读取大文件内容至内存中，直接传递本地绝对 URI 给 Local Bot API
func (c *TelegramClient) SendDocument(ctx context.Context, chatID int64, fileURI string, caption string, parseMode string) (*Message, error) {
	reqData := map[string]any{
		"chat_id":  chatID,
		"document": fileURI,
	}
	if caption != "" {
		reqData["caption"] = caption
	}
	if parseMode != "" {
		reqData["parse_mode"] = parseMode
	}

	var res APIResponse[*Message]
	if err := c.postJSON(ctx, c.httpClient, "sendDocument", reqData, &res); err != nil {
		return nil, err
	}

	if !res.OK {
		return nil, fmt.Errorf("TG API 发送文件失败 [%d]: %s", res.ErrorCode, res.Description)
	}

	return res.Result, nil
}

// SendVideo sends a local video file using file:/// protocol with streaming support
func (c *TelegramClient) SendVideo(ctx context.Context, chatID int64, fileURI string, caption string, parseMode string) (*Message, error) {
	reqData := map[string]any{
		"chat_id":            chatID,
		"video":              fileURI,
		"supports_streaming": true,
	}
	if caption != "" {
		reqData["caption"] = caption
	}
	if parseMode != "" {
		reqData["parse_mode"] = parseMode
	}

	var res APIResponse[*Message]
	if err := c.postJSON(ctx, c.httpClient, "sendVideo", reqData, &res); err != nil {
		return nil, err
	}

	if !res.OK {
		return nil, fmt.Errorf("TG API 发送视频失败 [%d]: %s", res.ErrorCode, res.Description)
	}

	return res.Result, nil
}

// SendAudio sends a local audio file using file:/// protocol
func (c *TelegramClient) SendAudio(ctx context.Context, chatID int64, fileURI string, caption string, parseMode string) (*Message, error) {
	reqData := map[string]any{
		"chat_id": chatID,
		"audio":   fileURI,
	}
	if caption != "" {
		reqData["caption"] = caption
	}
	if parseMode != "" {
		reqData["parse_mode"] = parseMode
	}

	var res APIResponse[*Message]
	if err := c.postJSON(ctx, c.httpClient, "sendAudio", reqData, &res); err != nil {
		return nil, err
	}

	if !res.OK {
		return nil, fmt.Errorf("TG API 发送音频失败 [%d]: %s", res.ErrorCode, res.Description)
	}

	return res.Result, nil
}

// DeleteMessage deletes a message
func (c *TelegramClient) DeleteMessage(ctx context.Context, chatID int64, messageID int64) error {
	reqData := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}

	var res APIResponse[bool]
	if err := c.postJSON(ctx, c.httpClient, "deleteMessage", reqData, &res); err != nil {
		return err
	}
	return nil
}

// BotCommand represents a Telegram bot command definition
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetMyCommands registers bot commands to Telegram menu
func (c *TelegramClient) SetMyCommands(ctx context.Context, commands []BotCommand) error {
	reqData := map[string]any{
		"commands": commands,
	}

	var res APIResponse[bool]
	if err := c.postJSON(ctx, c.httpClient, "setMyCommands", reqData, &res); err != nil {
		return fmt.Errorf("设置命令菜单失败: %w", err)
	}

	if !res.OK {
		return fmt.Errorf("TG API 设置命令菜单失败 [%d]: %s", res.ErrorCode, res.Description)
	}

	return nil
}

// GetFile retrieves Telegram file metadata by file_id
func (c *TelegramClient) GetFile(ctx context.Context, fileID string) (*TGFile, error) {
	reqData := map[string]any{
		"file_id": fileID,
	}

	var res APIResponse[*TGFile]
	if err := c.postJSON(ctx, c.httpClient, "getFile", reqData, &res); err != nil {
		return nil, err
	}

	if !res.OK {
		return nil, fmt.Errorf("TG API 获取文件失败 [%d]: %s", res.ErrorCode, res.Description)
	}

	return res.Result, nil
}

// FileDownloadURL constructs the direct download URL for a file_path
func (c *TelegramClient) FileDownloadURL(filePath string) string {
	cleanPath := strings.TrimPrefix(filePath, "/")
	return fmt.Sprintf("%s/file/bot%s/%s", c.apiBase, c.token, cleanPath)
}

// DownloadFileStream downloads a file from Local Bot API with progress reporting
func (c *TelegramClient) DownloadFileStream(ctx context.Context, filePath string, dst io.Writer, onProgress func(downloaded, total int64)) error {
	downloadURL := c.FileDownloadURL(filePath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("构造下载请求失败: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求文件下载流失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载文件返回非 200 状态码: %d", resp.StatusCode)
	}

	totalBytes := resp.ContentLength
	buf := make([]byte, 32*1024) // 32KB 保持极低内存开销
	var downloaded int64

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			written, wErr := dst.Write(buf[:n])
			if wErr != nil {
				return fmt.Errorf("写入本地文件失败: %w", wErr)
			}
			downloaded += int64(written)
			if onProgress != nil {
				onProgress(downloaded, totalBytes)
			}
		}

		if rErr != nil {
			if rErr == io.EOF {
				break
			}
			return fmt.Errorf("读取网络流失败: %w", rErr)
		}
	}

	return nil
}
