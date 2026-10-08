package webui

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestEmbeddedUI(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Welcome back") || !strings.Contains(w.Body.String(), "Create account") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("embedded UI cache policy = %q, want no-store", w.Header().Get("Cache-Control"))
	}
}

func TestClientRendersUserContentAsText(t *testing.T) {
	raw, err := os.ReadFile("assets/conversation.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	if !strings.Contains(script, "text.textContent = message.text") || strings.Contains(script, "innerHTML") {
		t.Fatal("client must render chat content with textContent only")
	}
}

func TestDirectMessageNavigationAssetsAreEmbedded(t *testing.T) {
	html, err := os.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{`id="rail-home"`, `id="rail-lobby"`, `id="dm-list"`, `id="new-dm-dialog"`, `id="friends-pending-view"`, `id="conversation-view"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing client navigation element %s", want)
		}
	}
	for _, want := range []string{`id="server-rail"`, `id="add-server-button"`, `id="server-context"`, `id="server-invites-view"`, `id="server-settings-dialog"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing server navigation element %s", want)
		}
	}
	for _, name := range []string{"navigation.js", "friends.js", "direct-messages.js", "conversation.js", "servers.js"} {
		response := httptest.NewRecorder()
		Handler().ServeHTTP(response, httptest.NewRequest("GET", "/"+name, nil))
		if response.Code != 200 || response.Body.Len() == 0 {
			t.Fatalf("asset %s status=%d size=%d", name, response.Code, response.Body.Len())
		}
	}
	if strings.Contains(page, "Discord") || strings.Contains(page, "Active Now") {
		t.Fatal("Relay client must not copy Discord branding or its optional Active Now panel")
	}
}

func TestConversationComposerUsesItsOwnCompactGridRow(t *testing.T) {
	css, err := os.ReadFile("assets/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	styles := string(css)
	if !strings.Contains(styles, `grid-template-areas:"header" "banner" "messages" "composer"`) ||
		!strings.Contains(styles, `.composer{grid-area:composer;align-self:end;min-height:50px`) ||
		!strings.Contains(styles, `.composer input{height:36px`) {
		t.Fatal("conversation composer must remain in a compact dedicated bottom row")
	}
}

func TestReleaseAssetsUseOneCacheBustingVersion(t *testing.T) {
	html, err := os.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	app, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/styles.css?v=0.7.4", "/friends.css?v=0.7.4", "/app.js?v=0.7.4", "/notification.wav?v=0.7.4"} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("missing versioned asset %s", want)
		}
	}
	if strings.Count(string(app), "?v=0.7.4") != 10 {
		t.Fatal("all application module imports must share the release cache-busting version")
	}
}

func TestNotificationSoundAndMentionClientAreEmbedded(t *testing.T) {
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest("GET", "/notification.wav", nil))
	if response.Code != 200 || response.Body.Len() < 1000 {
		t.Fatalf("notification sound status=%d size=%d", response.Code, response.Body.Len())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "audio") {
		t.Fatalf("notification sound content type = %q", contentType)
	}
	app, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(app)
	for _, want := range []string{"renderMentionSuggestions", "selectMention", "playDirectMessageSound", "showServerInvitationAlert", "playNotificationSound", "serverUI.recordChannelActivity"} {
		if !strings.Contains(source, want) {
			t.Fatalf("missing notification client behavior %s", want)
		}
	}
	if strings.Contains(source, "mode === \"all\" && !mentioned") {
		t.Fatal("ordinary channel messages must not produce a popup or sound")
	}
	for _, forbidden := range []string{"sent you a direct message:", "in ${entry.server.name}"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("chat messages must not create centered popup text: %s", forbidden)
		}
	}
}

func TestServerCreateKeepsFormReferenceAcrossAwait(t *testing.T) {
	script, err := os.ReadFile("assets/servers.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	if !strings.Contains(source, "const form = event.currentTarget;") || strings.Contains(source, "event.currentTarget.reset()") {
		t.Fatal("server creation must retain its form before awaiting the API response")
	}
}

func TestUsernameInputsNormalizeBrowserAutofill(t *testing.T) {
	html, err := os.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("assets/auth.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `pattern="[a-z0-9_]+"`) || !strings.Contains(string(html), `autocapitalize="none"`) {
		t.Fatal("username inputs must expose lowercase ASCII browser constraints")
	}
	if !strings.Contains(string(script), `normalize("NFKC")`) || !strings.Contains(string(script), `replace(/[^a-z0-9_]/g, "")`) {
		t.Fatal("client must normalize pasted and autofilled usernames")
	}
	if !strings.Contains(string(script), `querySelectorAll("[data-relay-field][name]")`) {
		t.Fatal("client must serialize only Relay-owned form fields")
	}
	if strings.Count(string(html), "data-relay-field") < 17 {
		t.Fatal("every account form field must be marked as Relay-owned")
	}
}
