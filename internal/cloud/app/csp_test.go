package app

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	path := filepath.Join(append([]string{"..", "..", "..", "cloud"}, parts...)...)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return content
}

func TestTheThemeScriptHashMatchesTheScriptTheDashboardInlines(t *testing.T) {
	script := repoFile(t, "dashboard", "src", "theme-init.js")
	sum := sha256.Sum256(script)
	want := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	if themeScriptHash != want {
		t.Errorf("the Content-Security-Policy publishes %s but dashboard/src/theme-init.js hashes to %s.\n"+
			"Every page would drop its theme script, so a dark-mode reader would see a light flash on load.",
			themeScriptHash, want)
	}
}

func TestTheDashboardInlinesTheHashedThemeScript(t *testing.T) {
	script := string(repoFile(t, "dashboard", "src", "theme-init.js"))
	index := string(repoFile(t, "dashboard", "index.html"))

	if !strings.Contains(index, "<script>"+script+"</script>") {
		t.Error("dashboard/index.html no longer inlines dashboard/src/theme-init.js verbatim, so the policy would drop it.")
	}
}

func TestTheContentSecurityPolicyNamesNoThirdPartyFontOrigin(t *testing.T) {
	rec := httptest.NewRecorder()
	handler(testApp(nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	policy := rec.Header().Get("Content-Security-Policy")

	for _, origin := range []string{"fonts.googleapis.com", "fonts.gstatic.com"} {
		if strings.Contains(policy, origin) {
			t.Errorf("the policy still allows %s, but the dashboard self-hosts its fonts", origin)
		}
	}
	if !strings.Contains(policy, themeScriptHash) {
		t.Errorf("the policy does not publish the theme script hash: %s", policy)
	}
}
