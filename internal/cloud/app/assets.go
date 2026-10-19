package app

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"regexp"
)

var versionedAssets = []string{
	"/favicon.ico",
	"/favicon.svg",
	"/favicon-32x32.png",
	"/favicon-16x16.png",
	"/apple-touch-icon.png",
}

type assetVersions map[string]string

func newAssetVersions(files fs.FS) assetVersions {
	versions := make(assetVersions, len(versionedAssets))
	for _, asset := range versionedAssets {
		content, err := fs.ReadFile(files, asset[1:])
		if err != nil {
			continue
		}
		sum := sha256.Sum256(content)
		versions[asset] = asset + "?v=" + hex.EncodeToString(sum[:])[:12]
	}
	return versions
}

func (v assetVersions) rewrite(document []byte) []byte {
	for asset, versioned := range v {
		pattern := regexp.MustCompile(regexp.QuoteMeta(asset) + `(\?v=[^"']*)?`)
		document = pattern.ReplaceAllLiteral(document, []byte(versioned))
	}
	return document
}
