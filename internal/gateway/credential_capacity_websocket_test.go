package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/state"
)

func TestCredentialCapacityWebsocketMultiplexTurnLifetimeAndRPM(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(finish) })
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request struct {
				StreamID string `json:"stream_id"`
			}
			if conn.ReadJSON(&request) != nil {
				return
			}
			n := calls.Add(1)
			if n == 1 {
				close(started)
				<-finish
			}
			if conn.WriteMessage(websocket.TextMessage, websocketCompleted(fmt.Sprintf("resp_%d", n), request.StreamID)) != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	defer unblock()
	h, engine, input := websocketTestHandler(t, upstream.URL, channel.OpenAI)
	input.SystemSettings = config.Settings{state.SettingCredentialConcurrencyLimit: 1, state.SettingCredentialRPMLimit: 2}
	if _, err := h.manager.Publish(input); err != nil {
		t.Fatal(err)
	}
	sink := &recordingRequestLogSink{}
	h.requestLogSink = sink
	server := httptest.NewServer(engine)
	defer server.Close()
	conn := dialGatewayWebsocket(t, server.URL)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	send := func(lane string) {
		t.Helper()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","stream_id":%q,"model":"public","input":"hello","store":false}`, lane))); err != nil {
			t.Fatal(err)
		}
	}
	readCode := func() string {
		t.Helper()
		var response struct{ Error struct{ Code string } }
		if err := conn.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
		return response.Error.Code
	}
	send("a")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not dispatch")
	}
	send("b")
	if code := readCode(); code != "credential_concurrency_limit_exceeded" || calls.Load() != 1 {
		t.Fatalf("multiplex admission: %q calls=%d", code, calls.Load())
	}
	unblock()
	if code := readCode(); code != "" {
		t.Fatalf("active turn failed: %s", code)
	}
	waitWebsocketLogs(t, sink, 2)
	send("c")
	if code := readCode(); code != "" {
		t.Fatalf("idle connection retained slot or rejected turn consumed RPM: %s", code)
	}
	waitWebsocketLogs(t, sink, 3)
	send("d")
	if code := readCode(); code != "credential_rate_limit_exceeded" || calls.Load() != 2 {
		t.Fatalf("turn RPM boundary: %q calls=%d", code, calls.Load())
	}
}

func TestCredentialCapacityLiveTransfersUntilSessionTermination(t *testing.T) {
	fake := &liveFakeOpener{}
	h, engine, _, _, _ := liveGatewayFixture(t, fake)
	setLiveGroupMode(h, 1, state.CodexLiveDirect)
	h.manager.Current().Settings.CredentialConcurrencyLimit = 1
	delete(h.manager.Current().Groups, 2)
	server := httptest.NewServer(engine)
	defer server.Close()
	defer h.CloseCodexLive()
	_, offer := liveClientOffer(t)
	first := liveRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/live", "gl-client", "application/sdp", []byte(offer))
	defer first.Body.Close()
	if first.StatusCode != 201 {
		t.Fatalf("first live admission: %d", first.StatusCode)
	}
	second := liveRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/live", "gl-client", "application/sdp", []byte(offer))
	defer second.Body.Close()
	if second.StatusCode != 429 {
		t.Fatalf("live session did not own credential slot: %d", second.StatusCode)
	}
	call, exists := h.liveSessions.lookup("rtc_1", 1)
	if !exists {
		t.Fatal("missing live session")
	}
	if err := h.liveSessions.finishCall(call, "client_hangup"); err != nil {
		t.Fatal(err)
	}
	third := liveRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/live", "gl-client", "application/sdp", []byte(offer))
	defer third.Body.Close()
	if third.StatusCode != 201 {
		t.Fatalf("terminated live session retained slot: %d", third.StatusCode)
	}
}
