package mirasimstatus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"gpt-load/internal/platform/httpclient"
)

const (
	requestTimeout   = 8 * time.Second
	maxResponseBytes = int64(1 << 20)
)

type client struct {
	httpClient    *http.Client
	manager       *httpclient.HTTPClientManager
	proxyProvider httpclient.OutboundProxyProvider
}

// New creates a status reader using an injected transport. The client is copied:
// cookies, redirects and timeouts cannot weaken the public-feed policy. The
// transport must be trusted and must not inject application credentials.
func New(httpClient *http.Client) *Checker {
	if httpClient == nil {
		return NewWithProxy(nil, nil)
	}
	copyClient := *httpClient
	copyClient.Jar = nil
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout <= 0 || copyClient.Timeout > requestTimeout {
		copyClient.Timeout = requestTimeout
	}
	return newChecker(&client{httpClient: &copyClient})
}

// NewWithProxy uses the application's current system outbound proxy, resolved
// once for each fetch. Neither an endpoint nor caller headers are accepted.
func NewWithProxy(manager *httpclient.HTTPClientManager, provider httpclient.OutboundProxyProvider) *Checker {
	if manager == nil {
		manager = httpclient.NewHTTPClientManager()
	}
	return newChecker(&client{manager: manager, proxyProvider: provider})
}

func (c *client) forFetch() (*http.Client, bool, error) {
	if c.httpClient != nil {
		return c.httpClient, false, nil
	}
	config := &httpclient.Config{
		ConnectTimeout:        5 * time.Second,
		RequestTimeout:        requestTimeout,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		ResponseHeaderTimeout: 5 * time.Second,
		DisableCompression:    true,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableRedirects:      true,
	}
	if c.proxyProvider != nil {
		result, err := c.manager.NewClientForOutboundProxy(config, c.proxyProvider())
		return result, true, err
	}
	return c.manager.GetClient(config), false, nil
}

func (c *client) fetch(ctx context.Context) (*Data, error) {
	httpClient, scoped, err := c.forFetch()
	if err != nil {
		return nil, errors.New("Mirasim status: outbound proxy is unavailable")
	}
	if scoped {
		defer httpClient.CloseIdleConnections()
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, SourceURL, nil)
	if err != nil {
		return nil, errors.New("Mirasim status: could not create public request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "GPT-Load")
	response, err := httpClient.Do(request)
	if err != nil {
		// Do not expose arbitrary transport errors: they may include proxy secrets.
		switch {
		case errors.Is(err, context.Canceled):
			return nil, errors.New("Mirasim status: request canceled")
		case errors.Is(err, context.DeadlineExceeded):
			return nil, errors.New("Mirasim status: request timed out")
		default:
			return nil, errors.New("Mirasim status: public source request failed")
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Mirasim status: public source returned HTTP %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, errors.New("Mirasim status: invalid JSON content type")
	}
	if response.ContentLength > maxResponseBytes {
		return nil, errors.New("Mirasim status: response exceeds 1 MiB")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Mirasim status: read response: %v", ctx.Err())
		}
		return nil, errors.New("Mirasim status: could not read public response")
	}
	if int64(len(payload)) > maxResponseBytes {
		return nil, errors.New("Mirasim status: response exceeds 1 MiB")
	}
	return decodeData(payload)
}
