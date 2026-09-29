package mirasim

import "testing"

func TestRelaySessionFollowsContinuityKey(t *testing.T) {
	bind := func(accountID, continuity string) *Client {
		client := NewClient(Storage{AccountID: accountID})
		client.BindContinuity(continuity)
		return client
	}
	sessionOf := func(t *testing.T, client *Client) string {
		t.Helper()
		metadata, errMetadata := client.relayMetadataLocked("/v1/responses")
		if errMetadata != nil {
			t.Fatalf("relayMetadataLocked() error = %v", errMetadata)
		}
		if metadata[headerMirasimAgent] != "codex" {
			t.Fatalf("agent = %q, want codex", metadata[headerMirasimAgent])
		}
		return metadata[headerMirasimSession]
	}

	stable := sessionOf(t, bind("account-1", "conversation-a"))
	if stable == "" {
		t.Fatal("continuity session is empty")
	}
	if again := sessionOf(t, bind("account-1", "conversation-a")); again != stable {
		t.Fatalf("same conversation session = %q, want %q", again, stable)
	}
	if other := sessionOf(t, bind("account-1", "conversation-b")); other == stable {
		t.Fatalf("different conversations share session %q", other)
	}
	if otherAccount := sessionOf(t, bind("account-2", "conversation-a")); otherAccount == stable {
		t.Fatalf("different accounts share session %q", otherAccount)
	}

	unbound := sessionOf(t, NewClient(Storage{AccountID: "account-1"}))
	otherUnbound := sessionOf(t, NewClient(Storage{AccountID: "account-1"}))
	if unbound == "" || otherUnbound == "" || unbound == otherUnbound {
		t.Fatalf("unbound sessions = %q and %q, want distinct per-request values", unbound, otherUnbound)
	}
	if unbound == stable {
		t.Fatalf("unbound session reused the continuity session %q", unbound)
	}
}

func TestRelaySessionIgnoresBlankContinuityKey(t *testing.T) {
	client := NewClient(Storage{AccountID: "account-1"})
	client.BindContinuity("  ")
	first := mustSession(t, client)
	second := mustSession(t, NewClient(Storage{AccountID: "account-1"}))
	if first == second {
		t.Fatalf("blank continuity key produced the stable session %q", first)
	}
}

func TestClientForBindsRequestContinuityKey(t *testing.T) {
	client := clientFor(Storage{AccountID: "account-1"}, ExecuteRequest{ContinuityKey: "conversation-a"})
	if client.continuityKey != "conversation-a" {
		t.Fatalf("continuityKey = %q, want conversation-a", client.continuityKey)
	}
}

func mustSession(t *testing.T, client *Client) string {
	t.Helper()
	metadata, errMetadata := client.relayMetadataLocked("/v1/responses")
	if errMetadata != nil {
		t.Fatalf("relayMetadataLocked() error = %v", errMetadata)
	}
	return metadata[headerMirasimSession]
}
