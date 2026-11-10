package updater

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

const customReleaseURL = "https://github.com/SeventyfourSam/mihomo/releases/"

var customVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-custom(\.[1-9][0-9]*)?$`)

func customCoreBaseName(goos, goarch string) string {
	// The custom Windows package uses GOAMD64=v1 without a CPU suffix in its name.
	if goos == "windows" && goarch == "amd64" {
		return "mihomo-windows-amd64"
	}
	return ""
}

func customPackageURL(baseURL, version, packageName string) string {
	// Pin the package to the version just read, even if Latest changes meanwhile.
	return strings.TrimSuffix(baseURL, "latest/download/") + "download/" + version + "/" + packageName
}

func validateCustomVersion(version string) error {
	if !customVersionPattern.MatchString(version) {
		return fmt.Errorf("invalid custom core version %q", version)
	}
	return nil
}

func prepareCustomCore(packagePath, exePath string) error {
	// gzip -n omits the original name; the unpacker then keeps the version suffix.
	if strings.HasSuffix(packagePath, ".gz") {
		if _, err := os.Stat(exePath); os.IsNotExist(err) {
			if err := os.Rename(strings.TrimSuffix(packagePath, ".gz"), exePath); err != nil {
				return fmt.Errorf("locate unpacked custom core: %w", err)
			}
		}
	}
	// Check the replacement before the running executable is moved to its backup.
	info, err := os.Stat(exePath)
	if err != nil {
		return fmt.Errorf("check unpacked custom core: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("invalid unpacked custom core %q", exePath)
	}
	return nil
}
