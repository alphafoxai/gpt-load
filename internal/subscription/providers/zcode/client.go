package zcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	appVersion        = "3.14.3"
	userAgent         = "ZCode/" + appVersion
	zcodeRoot         = "https://zcode.z.ai"
	zaiAPI            = "https://api.z.ai"
	bigmodelAPI       = "https://bigmodel.cn"
	zaiAnthropic      = "https://api.z.ai/api/anthropic"
	bigmodelAnthropic = "https://open.bigmodel.cn/api/anthropic"
)

// Endpoints are the ZCode and coding-plan origins. Tests point them at one server.
type Endpoints struct {
	ZCode             string
	ZAIAPI            string
	BigModelAPI       string
	ZAIAnthropic      string
	BigModelAnthropic string
}

func ProductionEndpoints() Endpoints {
	return Endpoints{
		ZCode: zcodeRoot, ZAIAPI: zaiAPI, BigModelAPI: bigmodelAPI,
		ZAIAnthropic: zaiAnthropic, BigModelAnthropic: bigmodelAnthropic,
	}
}

func (e Endpoints) withDefaults() Endpoints {
	base := ProductionEndpoints()
	if e.ZCode == "" {
		e.ZCode = base.ZCode
	}
	if e.ZAIAPI == "" {
		e.ZAIAPI = base.ZAIAPI
	}
	if e.BigModelAPI == "" {
		e.BigModelAPI = base.BigModelAPI
	}
	if e.ZAIAnthropic == "" {
		e.ZAIAnthropic = base.ZAIAnthropic
	}
	if e.BigModelAnthropic == "" {
		e.BigModelAnthropic = base.BigModelAnthropic
	}
	return e
}

// Client calls ZCode's JSON envelope endpoints.
type Client struct {
	Endpoints Endpoints
	HTTP      *http.Client
}

func NewClient(endpoints Endpoints) *Client {
	return &Client{Endpoints: endpoints.withDefaults(), HTTP: &http.Client{Timeout: 25 * time.Second}}
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	if e == nil || e.Message == "" {
		return "ZCode request failed"
	}
	return e.Message
}

func (c *Client) call(ctx context.Context, method, url, auth, device string, body any, headers map[string]string) (gjson.Result, error) {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return gjson.Result{}, err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		return gjson.Result{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if device != "" && strings.HasPrefix(url, strings.TrimRight(c.Endpoints.ZCode, "/")+"/") {
		req.Header.Set("X-Device-Mid", device)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return gjson.Result{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return gjson.Result{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(gjson.GetBytes(raw, "msg").String())
		if message == "" {
			message = resp.Status
		}
		return gjson.Result{}, &apiError{Status: resp.StatusCode, Message: message}
	}
	if !gjson.ValidBytes(raw) {
		return gjson.Result{}, fmt.Errorf("ZCode response is not JSON")
	}
	code := gjson.GetBytes(raw, "code")
	if code.Exists() {
		text := code.String()
		if text != "" && text != "0" && text != "200" {
			message := strings.TrimSpace(gjson.GetBytes(raw, "msg").String())
			if message == "" {
				message = "error " + text
			}
			return gjson.Result{}, &apiError{Status: resp.StatusCode, Message: message}
		}
	}
	data := gjson.GetBytes(raw, "data")
	if !data.Exists() {
		return gjson.ParseBytes(raw), nil
	}
	return data, nil
}
