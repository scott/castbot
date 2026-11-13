package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// staticAsset is one embedded file as it is served.
// tag is the content hash of body, so a changed file changes the URL.
type staticAsset struct {
	body  []byte
	tag   string
	ctype string
}

// staticAssets is filled once at startup from the embedded tree.
var staticAssets map[string]staticAsset

func init() {
	assets, err := loadStaticAssets()
	if err != nil {
		panic(err)
	}
	staticAssets = assets
}

func loadStaticAssets() (map[string]staticAsset, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	raw := map[string][]byte{}
	err = fs.WalkDir(sub, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, name)
		if err != nil {
			return err
		}
		raw[name] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Hash fonts and scripts first so stylesheets can name those hashes.
	tags := map[string]string{}
	for name, body := range raw {
		if strings.HasSuffix(strings.ToLower(name), ".css") {
			continue
		}
		tags[name] = contentTag(body)
	}
	out := make(map[string]staticAsset, len(raw))
	for name, body := range raw {
		if strings.HasSuffix(strings.ToLower(name), ".css") {
			body = rewriteStaticCSS(name, body, tags)
		}
		tag := contentTag(body)
		out[name] = staticAsset{body: body, tag: tag, ctype: staticContentType(name)}
	}
	return out, nil
}

func contentTag(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:8])
}

func staticContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".woff2":
		return "font/woff2"
	default:
		if t := mime.TypeByExtension(path.Ext(name)); t != "" {
			return t
		}
		return "application/octet-stream"
	}
}

// cssURL matches a single url(...) token, including the quotes when present.
var cssURL = regexp.MustCompile(`url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s]+))\s*\)`)

// rewriteStaticCSS points relative url() targets at their content-hashed names.
// The stylesheet hash is taken after this rewrite, so the CSS URL changes when a font changes.
func rewriteStaticCSS(from string, body []byte, tags map[string]string) []byte {
	return cssURL.ReplaceAllFunc(body, func(match []byte) []byte {
		parts := cssURL.FindSubmatch(match)
		raw := string(firstNonEmpty(parts[1], parts[2], parts[3]))
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "data:") || strings.Contains(raw, "://") || strings.HasPrefix(raw, "#") {
			return match
		}
		ref := raw
		if i := strings.IndexAny(ref, "?#"); i >= 0 {
			ref = ref[:i]
		}
		if strings.HasPrefix(ref, "/") {
			ref = strings.TrimPrefix(ref, "/static/")
			ref = strings.TrimPrefix(ref, "/")
		} else {
			ref = path.Clean(path.Join(path.Dir(from), ref))
		}
		if !safeStaticName(ref) {
			return match
		}
		tag, ok := tags[ref]
		if !ok {
			return match
		}
		return []byte(`url("` + ref + `?v=` + tag + `")`)
	})
}

func firstNonEmpty(parts ...[]byte) []byte {
	for _, p := range parts {
		if len(p) > 0 {
			return p
		}
	}
	return nil
}

func safeStaticName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.HasPrefix(name, "../") && !strings.Contains(name, `\`)
}

// staticHref is the template helper for a versioned /static/ URL.
func staticHref(name string) string {
	name = strings.TrimPrefix(name, "/static/")
	name = strings.TrimPrefix(name, "/")
	name = path.Clean(name)
	asset, ok := staticAssets[name]
	if !ok || !safeStaticName(name) {
		if !safeStaticName(name) {
			return "/static/"
		}
		return "/static/" + name
	}
	return "/static/" + name + "?v=" + asset.tag
}

// staticRequest reports paths that are served from the embedded tree.
// Cleaned first so a /static/../ escape is not treated as static.
func staticRequest(p string) bool {
	if p == "" {
		return false
	}
	c := path.Clean(p)
	return c == "/static" || strings.HasPrefix(c, "/static/")
}

const (
	staticCacheImmutable  = "public, max-age=31536000, immutable"
	staticCacheRevalidate = "public, max-age=86400"
)

func staticHandler() http.Handler {
	return http.HandlerFunc(serveStatic)
}

func serveStatic(w http.ResponseWriter, r *http.Request) {
	name := path.Clean("/" + r.URL.Path)
	name = strings.TrimPrefix(name, "/")
	if !safeStaticName(name) {
		http.NotFound(w, r)
		return
	}
	asset, ok := staticAssets[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("v") == asset.tag {
		w.Header().Set("Cache-Control", staticCacheImmutable)
	} else {
		w.Header().Set("Cache-Control", staticCacheRevalidate)
	}
	w.Header().Set("ETag", `"`+asset.tag+`"`)
	if asset.ctype != "" {
		w.Header().Set("Content-Type", asset.ctype)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(asset.body))
}
