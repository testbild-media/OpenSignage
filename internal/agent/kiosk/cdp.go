package kiosk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"opensignage/internal/agent/safe"
)

// CDP is a minimal Chrome DevTools Protocol client for one page target.
type CDP struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	next    int64
	pending map[int64]chan cdpResponse
	done    chan struct{}
	err     error
}

type cdpResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// DialCDP connects to a DevTools WebSocket URL.
func DialCDP(ctx context.Context, wsURL string) (*CDP, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(64 << 20) // screenshots arrive base64 encoded
	c := &CDP{conn: conn, pending: map[int64]chan cdpResponse{}, done: make(chan struct{})}
	safe.Go("cdp-reader", c.readLoop)
	return c, nil
}

func (c *CDP) readLoop() {
	defer close(c.done)
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.mu.Lock()
			c.err = err
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var r cdpResponse
		if json.Unmarshal(data, &r) != nil || r.ID == 0 {
			continue // events are not used
		}
		c.mu.Lock()
		ch := c.pending[r.ID]
		delete(c.pending, r.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	}
}

// Call invokes a method and decodes the result into out (may be nil).
func (c *CDP) Call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return fmt.Errorf("cdp closed: %w", c.err)
	}
	c.next++
	id := c.next
	ch := make(chan cdpResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		c.forget(id)
		return err
	}
	select {
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	case r, ok := <-ch:
		if !ok {
			return errors.New("cdp connection closed")
		}
		if r.Error != nil {
			return fmt.Errorf("%s: %s (%d)", method, r.Error.Message, r.Error.Code)
		}
		if out != nil && len(r.Result) > 0 {
			return json.Unmarshal(r.Result, out)
		}
		return nil
	}
}

func (c *CDP) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Close ends the connection.
func (c *CDP) Close() {
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		_ = c.conn.CloseNow()
	}
}

// Closed reports whether the connection is gone.
func (c *CDP) Closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// Health evaluates a trivial expression in the page.
func (c *CDP) Health(ctx context.Context) error {
	var r struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		ExceptionDetails any `json:"exceptionDetails"`
	}
	if err := c.Call(ctx, "Runtime.evaluate", map[string]any{"expression": "document.readyState", "returnByValue": true}, &r); err != nil {
		return err
	}
	if r.ExceptionDetails != nil {
		return errors.New("page evaluation failed")
	}
	return nil
}

// Reload reloads the page bypassing the cache.
func (c *CDP) Reload(ctx context.Context) error {
	return c.Call(ctx, "Page.reload", map[string]any{"ignoreCache": true}, nil)
}

// Screenshot captures the page as JPEG (quality 70, at most 1920 px wide).
func (c *CDP) Screenshot(ctx context.Context) ([]byte, error) {
	var lm struct {
		CSSLayoutViewport struct {
			ClientWidth  float64 `json:"clientWidth"`
			ClientHeight float64 `json:"clientHeight"`
		} `json:"cssLayoutViewport"`
	}
	if err := c.Call(ctx, "Page.getLayoutMetrics", nil, &lm); err != nil {
		return nil, err
	}
	params := map[string]any{"format": "jpeg", "quality": 70, "fromSurface": true}
	if w, h := lm.CSSLayoutViewport.ClientWidth, lm.CSSLayoutViewport.ClientHeight; w > 0 && h > 0 {
		scale := 1.0
		if w > 1920 {
			scale = 1920 / w
		}
		params["clip"] = map[string]any{"x": 0, "y": 0, "width": w, "height": h, "scale": scale}
	}
	var r struct {
		Data string `json:"data"`
	}
	if err := c.Call(ctx, "Page.captureScreenshot", params, &r); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(r.Data)
}

// ReadDevToolsPort parses <user-data-dir>/DevToolsActivePort (first line: port).
func ReadDevToolsPort(userDataDir string) (int, error) {
	b, err := os.ReadFile(filepath.Join(userDataDir, "DevToolsActivePort"))
	if err != nil {
		return 0, err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	port, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid DevToolsActivePort content %q", line)
	}
	return port, nil
}

// PageWebSocketURL finds the page target (preferring one showing urlPrefix).
func PageWebSocketURL(ctx context.Context, port int, urlPrefix string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/list", port), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var targets []struct {
		Type                 string `json:"type"`
		URL                  string `json:"url"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return "", err
	}
	first := ""
	for _, t := range targets {
		if t.Type != "page" || t.WebSocketDebuggerURL == "" {
			continue
		}
		if strings.HasPrefix(t.URL, urlPrefix) {
			return t.WebSocketDebuggerURL, nil
		}
		if first == "" {
			first = t.WebSocketDebuggerURL
		}
	}
	if first == "" {
		return "", errors.New("no page target")
	}
	return first, nil
}
