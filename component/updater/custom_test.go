package updater

import (
	"archive/zip"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCustomCorePackages(t *testing.T) {
	const version = "v1.19.33-custom.3"
	for _, tc := range []struct {
		goos, goarch, name, extension string
	}{
		{"windows", "amd64", "mihomo-windows-amd64", ".zip"},
		{"darwin", "arm64", "mihomo-darwin-arm64", ".gz"},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			name := customCoreBaseName(tc.goos, tc.goarch)
			if name == "" {
				name = "mihomo-" + tc.goos + "-" + tc.goarch
			}
			if name != tc.name {
				t.Fatalf("base name = %q, want %q", name, tc.name)
			}
			if runtime.GOOS == tc.goos && runtime.GOARCH == tc.goarch && DefaultCoreUpdater.CoreBaseName() != tc.name {
				t.Fatal("CoreBaseName does not match the published custom package")
			}
			asset := name + "-" + version + tc.extension
			want := "https://github.com/SeventyfourSam/mihomo/releases/download/" + version + "/" + asset
			for _, base := range []string{baseReleaseURL, baseAlphaURL} {
				if got := customPackageURL(base, version, asset); got != want {
					t.Fatalf("package URL = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestCustomVersionMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"custom", "v1.19.33-custom.3\n", "v1.19.33-custom.3", http.StatusOK},
		{"crlf", "v1.19.33-custom.3\r\n", "v1.19.33-custom.3", http.StatusOK},
		{"legacy", "v1.19.31-custom", "v1.19.31-custom", http.StatusOK},
		{"upstream", "v1.19.33", "", http.StatusOK},
		{"alpha", "alpha-deadbeef", "", http.StatusOK},
		{"path", "v1.19.33-custom.3/../../other", "", http.StatusOK},
		{"empty", "", "", http.StatusOK},
		{"not-found", "v1.19.33-custom.3", "", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			got, err := DefaultCoreUpdater.getLatestVersion(server.URL)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("accepted invalid metadata: %q", got)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("version = %q, error = %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestCustomArchiveReplacement(t *testing.T) {
	for _, tc := range []struct {
		name, core, extension, entry, payload string
		valid                                 bool
	}{
		{"macos-gzip-n", "mihomo-darwin-arm64", ".gz", "", "new core", true},
		{"macos-named-gzip", "mihomo-darwin-arm64", ".gz", "mihomo-darwin-arm64", "new core", true},
		{"windows-zip", "mihomo-windows-amd64", ".zip", "mihomo-windows-amd64.exe", "new core", true},
		{"wrong-zip-entry", "mihomo-windows-amd64", ".zip", "other.exe", "new core", false},
		{"empty-zip-entry", "mihomo-windows-amd64", ".zip", "mihomo-windows-amd64.exe", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			current := filepath.Join(dir, "running-core")
			if err := os.WriteFile(current, []byte("old core"), 0o755); err != nil {
				t.Fatal(err)
			}
			packagePath := filepath.Join(dir, tc.core+"-v1.19.33-custom.3"+tc.extension)
			f, err := os.Create(packagePath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.extension == ".gz" {
				w := gzip.NewWriter(f)
				w.Name = tc.entry
				_, err = w.Write([]byte(tc.payload))
				if closeErr := w.Close(); err == nil {
					err = closeErr
				}
			} else {
				w := zip.NewWriter(f)
				entry, createErr := w.Create(tc.entry)
				err = createErr
				if err == nil {
					_, err = entry.Write([]byte(tc.payload))
				}
				if closeErr := w.Close(); err == nil {
					err = closeErr
				}
			}
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := DefaultCoreUpdater.unpack(dir, packagePath, 0o755); err != nil {
				t.Fatal(err)
			}
			exeName := tc.core
			if tc.extension == ".zip" {
				exeName += ".exe"
			}
			exePath := filepath.Join(dir, exeName)
			err = prepareCustomCore(packagePath, exePath)
			if !tc.valid {
				if err == nil {
					t.Fatal("accepted missing or empty core")
				}
				if b, _ := os.ReadFile(current); string(b) != "old core" {
					t.Fatal("invalid package changed the current core")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			backupDir := filepath.Join(dir, "backup")
			backup := filepath.Join(backupDir, "running-core")
			if err := DefaultCoreUpdater.backup(current, backup, backupDir); err != nil {
				t.Fatal(err)
			}
			if err := DefaultCoreUpdater.copyFile(exePath, current); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(current); string(b) != tc.payload {
				t.Fatal("replacement does not contain the new core")
			}
			if b, _ := os.ReadFile(backup); string(b) != "old core" {
				t.Fatal("backup does not contain the old core")
			}
		})
	}
}

func TestCustomDownloadHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing package", http.StatusNotFound)
	}))
	defer server.Close()
	dir := filepath.Join(t.TempDir(), "update")
	err := DefaultCoreUpdater.download(dir, filepath.Join(dir, "core.zip"), server.URL)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected HTTP error, got %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("failed download created an update directory")
	}
}
