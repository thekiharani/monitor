package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"

	"github.com/andybalholm/brotli"
	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/html"
	"github.com/tdewolff/minify/v2/js"
)

var mediaTypes = map[string]string{".html": "text/html", ".css": "text/css", ".js": "application/javascript"}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: webbuild <dir>")
	}
	m := minify.New()
	m.Add("text/html", &html.Minifier{KeepDocumentTags: true, KeepEndTags: true, KeepQuotes: true})
	m.AddFunc("text/css", css.Minify)
	m.AddFuncRegexp(regexp.MustCompile("^(application|text)/(x-)?(java|ecma)script$"), js.Minify)
	err := filepath.WalkDir(os.Args[1], func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		mediaType, ok := mediaTypes[filepath.Ext(path)]
		if !ok {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		small, err := m.Bytes(mediaType, source)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		gz, br := compress(small)
		for name, body := range map[string][]byte{path: small, path + ".gz": gz, path + ".br": br} {
			if err := os.WriteFile(name, body, 0o644); err != nil {
				return err
			}
		}
		fmt.Printf("%-24s %7d → %6d minified, %6d gzip, %6d brotli\n", filepath.Base(path), len(source), len(small), len(gz), len(br))
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
}

func compress(body []byte) (gz, br []byte) {
	var g, b bytes.Buffer
	gw, _ := gzip.NewWriterLevel(&g, gzip.BestCompression)
	gw.Write(body)
	gw.Close()
	bw := brotli.NewWriterLevel(&b, brotli.BestCompression)
	bw.Write(body)
	bw.Close()
	return g.Bytes(), b.Bytes()
}
