package dialect

import "testing"

func TestAnthropicInspectsClaudeCodeSessionID(t *testing.T) {
	t.Parallel()
	const session = "0f8fad5b-d9cb-469f-a165-70867728950e"
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "structured user id",
			body: `{"model":"m","metadata":{"user_id":"{\"device_id\":\"` + "ab12" + `\",\"account_uuid\":\"\",\"session_id\":\"` + session + `\"}"},"messages":[{"role":"user","content":"hi"}]}`,
			want: session,
		},
		{
			name: "legacy user id",
			body: `{"model":"m","metadata":{"user_id":"user_9f86d081884c7d65_account_6ba7b810-9dad-11d1-80b4-00c04fd430c8_session_` + session + `"},"messages":[{"role":"user","content":"hi"}]}`,
			want: session,
		},
		{
			name: "upper case uuid is normalized",
			body: `{"model":"m","metadata":{"user_id":"user_x_account__session_0F8FAD5B-D9CB-469F-A165-70867728950E"},"messages":[{"role":"user","content":"hi"}]}`,
			want: session,
		},
		{
			// 普通客户端的 user_id 是终端用户身份，不是会话；不能当作会话绑定。
			name: "plain end user id",
			body: `{"model":"m","metadata":{"user_id":"customer-42"},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "session suffix without claude code prefix",
			body: `{"model":"m","metadata":{"user_id":"tenant_session_` + session + `"},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "structured session is not a uuid",
			body: `{"model":"m","metadata":{"user_id":"{\"session_id\":\"abc\"}"},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "user id is not a string",
			body: `{"model":"m","metadata":{"user_id":{"session_id":"` + session + `"}},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "no metadata",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			metadata, err := NewAnthropic().InspectRequest(&ParsedRequest{Method: "POST", Path: "/v1/messages", Body: []byte(test.body)})
			if err != nil {
				t.Fatalf("InspectRequest() error = %v", err)
			}
			if metadata.ClaudeCodeSessionID != test.want {
				t.Fatalf("ClaudeCodeSessionID = %q, want %q", metadata.ClaudeCodeSessionID, test.want)
			}
		})
	}
}
