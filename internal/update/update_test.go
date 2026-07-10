package update

import "testing"

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.6.0", "v0.6.1", true},
		{"v0.6.0", "v0.7.0", true},
		{"v0.6.0", "v1.0.0", true},
		{"v0.6.0", "v0.6.0", false},
		{"v0.6.1", "v0.6.0", false},
		{"v1.0.0", "v0.9.9", false},
		{"0.6.0", "v0.6.1", true}, // tolerate missing 'v'
		{"local_abc", "v0.6.1", false},
		{"v0.6.0", "garbage", false},
	}
	for _, c := range cases {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestSetupAsset(t *testing.T) {
	rel := &ghRelease{}
	rel.Assets = append(rel.Assets, struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}{Name: "checksums.txt", URL: "https://example/c.txt"})
	rel.Assets = append(rel.Assets, struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}{Name: "quartermaster-setup-v0.6.1.exe", URL: "https://github.com/x/y/releases/download/v0.6.1/s.exe"})

	if got := setupAsset(rel); got != "https://github.com/x/y/releases/download/v0.6.1/s.exe" {
		t.Fatalf("setupAsset got %q", got)
	}
}

func TestValidAssetURL(t *testing.T) {
	ok := []string{
		"https://github.com/o/r/releases/download/v1/s.exe",
		"https://objects.githubusercontent.com/x",
	}
	bad := []string{
		"http://github.com/o/r/s.exe",       // not https
		"https://evil.com/github.com/s.exe", // wrong host
		"https://githubXcom/s.exe",
	}
	for _, u := range ok {
		if err := validAssetURL(u); err != nil {
			t.Errorf("validAssetURL(%q) unexpected err: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := validAssetURL(u); err == nil {
			t.Errorf("validAssetURL(%q) expected err", u)
		}
	}
}
