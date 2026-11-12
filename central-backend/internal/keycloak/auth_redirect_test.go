package keycloak

import (
	"net/url"
	"testing"
)

func TestGetRedirectUrlForSignInForcesAccountPicker(t *testing.T) {
	t.Setenv("CENTRAL_UI_HOST", "https://ui.example.com")
	t.Setenv("CENTRAL_BACKEND_HOST", "https://backend.example.com")
	got := NewKeycloakAuthService().GetRedirectUrlForSignIn("azure")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("prompt") != "select_account" {
		t.Fatalf("prompt = %q, want select_account so a denied SSO session cannot silently retry", q.Get("prompt"))
	}
	if q.Get("kc_idp_hint") != "azure" {
		t.Fatalf("kc_idp_hint = %q, want azure", q.Get("kc_idp_hint"))
	}
	if q.Get("redirect_uri") != "https://ui.example.com/signin" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

func TestGetRedirectUrlForSignOutIncludesIDTokenHint(t *testing.T) {
	t.Setenv("CENTRAL_UI_HOST", "https://ui.example.com")
	t.Setenv("CENTRAL_BACKEND_HOST", "https://backend.example.com")
	kc := NewKeycloakAuthService()

	withHint, err := url.Parse(kc.GetRedirectUrlForSignOut("id-token"))
	if err != nil {
		t.Fatal(err)
	}
	if withHint.Query().Get("id_token_hint") != "id-token" {
		t.Fatalf("id_token_hint = %q, want id-token so logout reaches the IdP", withHint.Query().Get("id_token_hint"))
	}

	withoutHint, err := url.Parse(kc.GetRedirectUrlForSignOut(""))
	if err != nil {
		t.Fatal(err)
	}
	if withoutHint.Query().Has("id_token_hint") {
		t.Fatalf("empty idToken still set id_token_hint=%q", withoutHint.Query().Get("id_token_hint"))
	}
}
