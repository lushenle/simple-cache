package main

import (
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

//go:embed admin-dist/*
var adminStatic embed.FS

func adminSPAHandler() http.Handler {
	sub, err := fs.Sub(adminStatic, "admin-dist")
	if err != nil {
		panic("admin-dist not found in embedded filesystem")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get the file path relative to admin-dist root.
		p := strings.TrimPrefix(r.URL.Path, "/admin")
		p = strings.TrimPrefix(p, "/")
		if p == "" {
			p = "index.html"
		}

		f, err := sub.Open(p)
		if err != nil {
			// SPA fallback: serve index.html for client-side routes.
			f, err = sub.Open("index.html")
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
		}
		defer f.Close()

		// Set Content-Type based on file extension.
		ext := filepath.Ext(p)
		if ct := mime.TypeByExtension(ext); ct != "" {
			w.Header().Set("Content-Type", ct)
		}

		// Serve the file content.
		stat, err := f.Stat()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, stat.Name(), stat.ModTime(), f.(io.ReadSeeker))
	})
}
