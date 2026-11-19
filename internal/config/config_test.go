package config

import "testing"

func TestParseInboxRoutes(t *testing.T) {
	routes, err := ParseInboxRoutes("Inbox:gemini, Inbox-Local:ollama")
	if err != nil {
		t.Fatalf("ParseInboxRoutes failed: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	if routes[0].InboxFolder != "Inbox" || routes[0].AIProvider != "gemini" {
		t.Errorf("unexpected route[0]: %+v", routes[0])
	}
	if routes[1].InboxFolder != "Inbox-Local" || routes[1].AIProvider != "ollama" {
		t.Errorf("unexpected route[1]: %+v", routes[1])
	}

	if _, err := ParseInboxRoutes("InvalidEntry"); err == nil {
		t.Errorf("expected error for malformed route")
	}
}
