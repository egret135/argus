package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/admin/argus/internal/model"
)

// FeishuWorker sends alert messages to a Feishu webhook.
type FeishuWorker struct {
	webhookURL  string
	secret      string
	baseURL     string
	maxRetries  int
	requestChan <-chan model.AlertRequest
	resultChan  chan<- model.AlertResult
	client      *http.Client
	ctx         context.Context
}

// NewFeishuWorker creates a new FeishuWorker.
func NewFeishuWorker(webhookURL, secret, baseURL string, maxRetries int, requestChan <-chan model.AlertRequest, resultChan chan<- model.AlertResult) *FeishuWorker {
	return &FeishuWorker{
		webhookURL:  webhookURL,
		secret:      secret,
		baseURL:     baseURL,
		maxRetries:  maxRetries,
		requestChan: requestChan,
		resultChan:  resultChan,
		client:      &http.Client{Timeout: 10 * time.Second},
	}
}

// genSign computes the Feishu webhook signature: base64(HMAC-SHA256(secret, timestamp+"\n"+secret)).
func genSign(secret string, timestamp int64) string {
	stringToSign := strconv.FormatInt(timestamp, 10) + "\n" + secret
	h := hmac.New(sha256.New, []byte(stringToSign))
	h.Write([]byte{})
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// Run reads from requestChan and sends alerts until ctx is done or the channel is closed.
func (w *FeishuWorker) Run(ctx context.Context) {
	w.ctx = ctx
	for {
		select {
		case <-ctx.Done():
			return
		case req, ok := <-w.requestChan:
			if !ok {
				return
			}
			w.handle(ctx, req)
		}
	}
}

func (w *FeishuWorker) handle(ctx context.Context, req model.AlertRequest) {
	result := model.AlertResult{
		Fingerprint: req.Fingerprint,
		CooldownKey: req.CooldownKey,
	}

	defer func() {
		if r := recover(); r != nil {
			result.Success = false
		}
		select {
		case w.resultChan <- result:
		case <-ctx.Done():
		}
	}()

	err := w.send(ctx, req.LogEntry)
	result.Success = err == nil
}

func (w *FeishuWorker) send(ctx context.Context, entry model.LogEntry) error {
	// Build detail link
	detailURL := ""
	if w.baseURL != "" {
		detailURL = w.baseURL + "/admin/log?seq_id=" + strconv.FormatUint(entry.SeqID, 10)
	}

	// Card header template based on level
	headerTitle := "🔴 错误告警"
	headerTemplate := "red"
	if entry.Level == "FATAL" {
		headerTitle = "💀 致命告警"
		headerTemplate = "red"
	}

	// Build card elements
	elements := []any{
		map[string]any{
			"tag": "div",
			"fields": []map[string]any{
				{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": "**📊 级别**\n" + entry.Level}},
				{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": "**🖥 服务**\n" + entry.Service}},
			},
		},
		map[string]any{
			"tag": "div",
			"fields": []map[string]any{
				{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": "**🕐 时间**\n" + entry.Timestamp}},
				{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": "**📁 来源**\n" + entry.Source}},
			},
		},
		map[string]any{"tag": "hr"},
		map[string]any{
			"tag":  "div",
			"text": map[string]string{"tag": "lark_md", "content": "**📝 错误信息**"},
		},
		map[string]any{
			"tag":  "div",
			"text": map[string]string{"tag": "plain_text", "content": entry.Message},
		},
	}

	// Add action button if we have a detail URL
	if detailURL != "" {
		elements = append(elements,
			map[string]any{"tag": "hr"},
			map[string]any{
				"tag": "action",
				"actions": []map[string]any{
					{
						"tag":  "button",
						"text": map[string]string{"tag": "plain_text", "content": "📋 查看日志详情"},
						"type": "primary",
						"url":  detailURL,
					},
				},
			},
		)
	}

	msg := map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"header": map[string]any{
				"title":    map[string]string{"tag": "plain_text", "content": headerTitle},
				"template": headerTemplate,
			},
			"elements": elements,
		},
	}

	if w.secret != "" {
		ts := time.Now().Unix()
		msg["timestamp"] = strconv.FormatInt(ts, 10)
		msg["sign"] = genSign(w.secret, ts)
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal feishu message: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= w.maxRetries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if attempt > 0 {
			backoff := time.Second * (1 << (attempt - 1)) // 1s, 2s, 4s, 8s, 16s
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.webhookURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := w.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("feishu request: %w", err)
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var result struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
			}
			if err := json.Unmarshal(respBody, &result); err == nil && result.Code != 0 {
				lastErr = fmt.Errorf("feishu api error: code=%d msg=%s", result.Code, result.Msg)
				continue
			}
			return nil
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("feishu responded %d", resp.StatusCode)
			continue
		}
		// 4xx (except 429): don't retry
		return fmt.Errorf("feishu responded %d", resp.StatusCode)
	}
	return lastErr
}
