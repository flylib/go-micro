package inspect

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
	"net/url"
	"strconv"
	"time"
)

// Webhook kinds.
const (
	KindJSON     = "json"     // the Event as JSON
	KindFeishu   = "feishu"   // Feishu / Lark custom bot
	KindDingTalk = "dingtalk" // DingTalk custom robot
	KindWeCom    = "wecom"    // WeCom (WeChat Work) group robot
)

// Webhook posts events to a URL.
type Webhook struct {
	URL    string
	Kind   string // KindJSON (default), KindFeishu, KindDingTalk or KindWeCom
	Secret string // signing secret of a Feishu or DingTalk bot with "signature" security on
	Client *http.Client
	now    func() time.Time
}

// Notify sends one event.
func (w *Webhook) Notify(ctx context.Context, e Event) error {
	u, body, err := w.request(e)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c := w.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	rsp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer rsp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(rsp.Body, 4096))
	if rsp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook %s: %s: %s", w.Kind, rsp.Status, b)
	}
	// the bots answer 200 with an error code in the body
	var r struct {
		Code    *int   `json:"code"`    // feishu
		ErrCode *int   `json:"errcode"` // dingtalk, wecom
		Msg     string `json:"msg"`
		ErrMsg  string `json:"errmsg"`
	}
	if json.Unmarshal(b, &r) == nil {
		if r.Code != nil && *r.Code != 0 {
			return fmt.Errorf("webhook %s: code %d: %s", w.Kind, *r.Code, r.Msg)
		}
		if r.ErrCode != nil && *r.ErrCode != 0 {
			return fmt.Errorf("webhook %s: errcode %d: %s", w.Kind, *r.ErrCode, r.ErrMsg)
		}
	}
	return nil
}

// Text renders an event for people.
func Text(e Event) string {
	icon := map[State]string{StateOK: "✅", StateUnhealthy: "⚠️", StateUnreachable: "🔴", StateGone: "⚪"}[e.To]
	s := fmt.Sprintf("%s [go-micro] %s %s: %s → %s\nnode %s %s", icon, e.Node.Service, e.Node.Version, e.From, e.To, e.Node.ID, e.Node.Address)
	if e.Node.Error != "" && e.To != StateOK {
		s += "\nerror: " + e.Node.Error
	}
	if e.To != StateOK && e.To != StateGone {
		s += fmt.Sprintf("\n%d failed checks in a row", e.Node.Failures)
	}
	return s + "\n" + e.Time.Format(time.RFC3339)
}

func (w *Webhook) request(e Event) (string, []byte, error) {
	now := time.Now
	if w.now != nil {
		now = w.now
	}
	text := Text(e)
	u := w.URL
	var msg any
	switch w.Kind {
	case "", KindJSON:
		msg = e
	case KindFeishu:
		m := map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
		if w.Secret != "" {
			ts := strconv.FormatInt(now().Unix(), 10)
			// feishu: HMAC-SHA256 keyed with "timestamp\nsecret" over nothing
			mac := hmac.New(sha256.New, []byte(ts+"\n"+w.Secret))
			m["timestamp"] = ts
			m["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
		msg = m
	case KindDingTalk:
		msg = map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
		if w.Secret != "" {
			ts := strconv.FormatInt(now().UnixMilli(), 10)
			// dingtalk: HMAC-SHA256 keyed with the secret over "timestamp\nsecret"
			mac := hmac.New(sha256.New, []byte(w.Secret))
			mac.Write([]byte(ts + "\n" + w.Secret))
			pu, err := url.Parse(u)
			if err != nil {
				return "", nil, err
			}
			q := pu.Query()
			q.Set("timestamp", ts)
			q.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
			pu.RawQuery = q.Encode()
			u = pu.String()
		}
	case KindWeCom:
		msg = map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
	default:
		return "", nil, fmt.Errorf("webhook: unknown kind %q (want json, feishu, dingtalk or wecom)", w.Kind)
	}
	b, err := json.Marshal(msg)
	return u, b, err
}
