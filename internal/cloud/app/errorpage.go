package app

import (
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/thekiharani/monitor/internal/cloud/httpx"
)

type errorCopy struct {
	Badge, Title, Message string
	Tip                   string
}

func errorCopyByStatus(status int) errorCopy {
	switch status {
	case http.StatusBadRequest:
		return errorCopy{"Bad request", "That request doesn't look right", "The server couldn't understand what you sent. Check the link and try again.", "Tip: a stray space or a & that should be %26 can turn a good URL into a bad one."}
	case http.StatusUnauthorized:
		return errorCopy{"Unauthorized", "Sign in required", "You need a valid session to open this page. Head back and sign in — it's passwordless.", "Tip: your session may have expired. Signing in again is all it takes."}
	case http.StatusForbidden:
		return errorCopy{"Forbidden", "You don't have access here", "This workspace is reserved for the account that owns it. Ask the owner if you think this is a mistake.", "Tip: signing in with the account that created the link usually fixes this."}
	case http.StatusNotFound:
		return errorCopy{"Page not found", "This link went off the map", "The page you're looking for doesn't exist, was moved, or never made it here.", "Tip: check the address for typos — a single swapped letter sends you somewhere else."}
	case http.StatusMethodNotAllowed:
		return errorCopy{"Method not allowed", "That pathway is closed", "This resource doesn't accept that type of request.", "Tip: use the same link and action the app gives you instead of typing a URL by hand."}
	case http.StatusGone:
		return errorCopy{"Gone", "It's no longer available", "This page used to exist but has been permanently removed. Nothing is broken — it's just gone.", "Tip: the link may have expired. Ask the person who shared it for a fresh one."}
	case http.StatusTooManyRequests:
		return errorCopy{"Too many requests", "Slow down a little", "That's too many requests in a short window. Give it a moment and try again.", "Tip: waiting a few seconds usually clears this. You're probably not stuck in a loop."}
	case http.StatusInternalServerError:
		return errorCopy{"Server error", "Something broke on our end", "An unexpected error hit the server. It wasn't your fault — we've logged it and are on it.", "Tip: reloading once often gets you through while we patch things up."}
	case http.StatusBadGateway:
		return errorCopy{"Bad gateway", "A link in the chain went down", "One of our upstream services didn't respond. We're already rerouting.", "Tip: a quick refresh usually lands you on a working path."}
	case http.StatusServiceUnavailable:
		return errorCopy{"Service unavailable", "We're down for a quick breather", "This service is temporarily unavailable, usually for maintenance. We'll be back shortly.", "Tip: hang tight and try again in a few minutes."}
	case http.StatusGatewayTimeout:
		return errorCopy{"Gateway timeout", "The upstream didn't make it", "A downstream service took too long and the gateway gave up.", "Tip: give it ten seconds and refresh — things settle quickly."}
	default:
		if status >= 400 && status < 500 {
			return errorCopy{"Request error", "This page isn't available", "We couldn't find or accept that request. Double-check the address and try again.", "Tip: if you typed the address, verify every character before retrying."}
		}
		return errorCopy{"Server error", "Oops — our side hit a snag", "Something went wrong while handling your request. We've been notified and will get it sorted.", "Tip: try again shortly — we're already looking into it."}
	}
}

func errorAccentCSS(status int) string {
	if status >= 500 {
		return ":root{--accent:#d96b7f;--accent-soft:#fae4e8;--accent-glow:#f4a9b6}"
	}
	return ":root{--accent:#d99a2b;--accent-soft:#fdf0d8;--accent-glow:#f0c883}"
}

func (a *App) notFound(w http.ResponseWriter, r *http.Request) {
	if a.dashboard != nil && httpx.HostMatches(r, a.cfg.DashboardOrigin) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		a.dashboard.serveSPAEntry(w, r)
		return
	}
	a.serveErrorPage(w, r, http.StatusNotFound)
}

func (a *App) serveErrorPage(w http.ResponseWriter, r *http.Request, status int) {
	writeErrorPage(w, r, status, a.cfg.AppOrigin, a.onMonitorSurface(r))
}

func (a *App) onMonitorSurface(r *http.Request) bool {
	return httpx.HostMatches(r, a.cfg.AppOrigin, a.cfg.DashboardOrigin, a.cfg.StatusOrigin)
}

type errorPage struct {
	Code, Badge, Title, Message, Tip, Path, Origin string
	Status                                         int
	OnMonitor                                      bool
}

func writeErrorPage(w http.ResponseWriter, r *http.Request, status int, origin string, onMonitor bool) {
	copy := errorCopyByStatus(status)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(status)

	path := html.EscapeString(r.URL.Path)
	if path == "" {
		path = "/"
	}

	_, _ = w.Write([]byte(errorPageHTML(errorPage{
		Code:      strconv.Itoa(status),
		Badge:     html.EscapeString(copy.Badge),
		Title:     html.EscapeString(copy.Title),
		Message:   html.EscapeString(copy.Message),
		Tip:       html.EscapeString(copy.Tip),
		Path:      path,
		Origin:    html.EscapeString(strings.TrimRight(origin, "/")),
		Status:    status,
		OnMonitor: onMonitor,
	})))
}

func errorPageHTML(p errorPage) string {
	code, badge, title, message, tip, path, status := p.Code, p.Badge, p.Title, p.Message, p.Tip, p.Path, p.Status
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<link rel="icon" type="image/svg+xml" href="` + p.link("/favicon.svg") + `">
<link rel="icon" sizes="any" href="` + p.link("/favicon.ico") + `">
<link rel="apple-touch-icon" href="` + p.link("/apple-touch-icon.png") + `">
<title>` + code + ` · Monitor</title>
<style>
:root{
  --bg:#f4fcfc; --fg:#031517; --muted:#4f696a; --card:rgba(255,255,255,.78);
  --border:#d4e5e6; --primary:#009498; --ring:#e5f6f5; --shadow:rgba(0,148,152,.14);
}
@media (prefers-color-scheme: dark){
  :root{ --bg:#000709; --fg:#eef7f8; --muted:#8b9c9d; --card:rgba(1,16,18,.72);
    --border:#162426; --primary:#00c8ca; --ring:#162426; --shadow:rgba(0,0,0,.55); }
}
` + errorAccentCSS(status) + `
*{box-sizing:border-box;margin:0;padding:0}
html,body{height:100%}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;
  color:var(--fg);background:var(--bg);display:flex;flex-direction:column;overflow-x:hidden;
  -webkit-font-smoothing:antialiased;line-height:1.5}
::selection{background:var(--accent-soft);color:var(--fg)}
a{color:inherit}
button{font:inherit}

/* ── Ambient background ─────────────────────────────── */
.ambient{position:fixed;inset:0;overflow:hidden;pointer-events:none;z-index:0}
.ambient::after{content:"";position:absolute;inset:0;
  background-image:radial-gradient(var(--ring) 1px,transparent 1px);background-size:26px 26px;
  -webkit-mask-image:radial-gradient(circle at 50% 42%,#000 0%,transparent 62%);mask-image:radial-gradient(circle at 50% 42%,#000 0%,transparent 62%);
  opacity:.5}
.blob{position:absolute;border-radius:50%;filter:blur(70px);opacity:.55;animation:drift 18s ease-in-out infinite}
.blob.a{width:520px;height:520px;left:12%;top:8%;background:radial-gradient(closest-side,var(--accent-soft),transparent 70%)}
.blob.b{width:460px;height:460px;right:6%;bottom:4%;background:radial-gradient(closest-side,var(--ring),transparent 70%);opacity:.5;animation-delay:-6s}
.blob.c{width:380px;height:380px;right:22%;top:12%;background:radial-gradient(closest-side,var(--accent-glow),transparent 70%);opacity:.32;animation-delay:-12s}
@keyframes drift{0%,100%{transform:translate(0,0) scale(1)}33%{transform:translate(26px,-24px) scale(1.08)}66%{transform:translate(-20px,20px) scale(.94)}}

/* ── Layout ─────────────────────────────────────────── */
header{position:relative;z-index:2;display:flex;align-items:center;justify-content:space-between;
  padding:22px 40px;max-width:1100px;margin:0 auto;width:100%}
.frame{display:flex;align-items:center;gap:11px;text-decoration:none;font-weight:800;letter-spacing:-.03em;font-size:20px}
.logo{width:34px;height:34px;border-radius:11px;display:grid;place-items:center;background:var(--primary);
  color:#fff;padding:7px;box-shadow:0 6px 16px var(--shadow);transition:transform .2s ease}
.logo svg{display:block;width:100%;height:100%}
.frame:hover .logo{transform:scale(1.06) rotate(-4deg)}
.brand-dot{color:var(--primary)}
.code-pill{font:600 13px/1 ui-monospace,SFMono-Regular,Menlo,monospace;letter-spacing:.03em;
  color:var(--muted);border:1px solid var(--border);border-radius:999px;padding:8px 16px;background:var(--card);
  backdrop-filter:blur(8px)}
main{position:relative;z-index:2;flex:1;display:flex;flex-direction:column;justify-content:center;
  padding:48px 24px 40px}
.wrap{max-width:720px;width:100%;margin:0 auto;text-align:center}
.hero{display:flex;flex-direction:column;align-items:center}
.hero>*{opacity:0;transform:translateY(22px);animation:rise .7s cubic-bezier(.22,1,.36,1) forwards}
@keyframes rise{to{opacity:1;transform:none}}
.hero>*:nth-child(1){animation-delay:.05s}
.hero>*:nth-child(2){animation-delay:.14s}
.hero>*:nth-child(3){animation-delay:.24s}
.hero>*:nth-child(4){animation-delay:.34s}
.hero>*:nth-child(5){animation-delay:.44s}
.hero>*:nth-child(6){animation-delay:.54s}

/* ── Badge ──────────────────────────────────────────── */
.badge{display:inline-flex;align-items:center;gap:9px;font:700 12px/1 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
  letter-spacing:.12em;text-transform:uppercase;color:var(--accent);border:1px solid color-mix(in srgb,var(--accent) 28%,transparent);
  background:var(--accent-soft);border-radius:999px;padding:9px 18px;box-shadow:0 8px 20px -8px var(--shadow)}
.badge::before{content:"";width:7px;height:7px;border-radius:50%;background:var(--accent);box-shadow:0 0 0 3px color-mix(in srgb,var(--accent) 22%,transparent)}

/* ── Radar + giant code ─────────────────────────────── */
.scene{position:relative;margin-top:34px;margin-bottom:6px;height:clamp(170px,30vw,250px);width:clamp(200px,34vw,300px)}
.rings{position:absolute;inset:0;display:grid;place-items:center}
.rings span{position:absolute;inset:0;border:1.5px solid color-mix(in srgb,var(--accent) 45%,transparent);
  border-radius:50%;animation:ping 3.6s cubic-bezier(.2,.6,.36,1) infinite}
.rings span:nth-child(2){animation-delay:1.2s}
.rings span:nth-child(3){animation-delay:2.4s}
@keyframes ping{0%{transform:scale(.2);opacity:0}18%{opacity:.9}100%{transform:scale(1.18);opacity:0}}
.code{position:absolute;inset:0;display:grid;place-items:center;font:900 clamp(96px,26vw,196px)/1 ui-monospace,SFMono-Regular,Menlo,monospace;
  letter-spacing:-.05em;
  background:linear-gradient(135deg,var(--fg) 0%,var(--muted) 60%,color-mix(in srgb,var(--muted) 55%,transparent) 100%);
  -webkit-background-clip:text;background-clip:text;color:transparent;
  filter:drop-shadow(0 14px 30px color-mix(in srgb,var(--fg) 18%,transparent));
  animation:float 6s ease-in-out infinite}
.code b{background:linear-gradient(135deg,var(--accent),color-mix(in srgb,var(--accent) 40%,var(--fg)));
  -webkit-background-clip:text;background-clip:text;color:transparent;font-weight:900}
@keyframes float{0%,100%{transform:translateY(0)}50%{transform:translateY(-9px)}}

/* ── Copy ───────────────────────────────────────────── */
h1{font-size:clamp(30px,6vw,46px);font-weight:800;letter-spacing:-.03em;line-height:1.08;margin-top:10px}
p{margin:16px auto 0;max-width:520px;color:var(--muted);font-size:16px;line-height:1.65}
.chip{margin:22px auto 0;display:inline-flex;align-items:center;gap:10px;max-width:100%;padding:9px 16px;border-radius:12px;
  border:1px dashed color-mix(in srgb,var(--muted) 40%,transparent);background:var(--card);backdrop-filter:blur(8px)}
.chip .label{font:600 12px/1 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:var(--muted);letter-spacing:.02em;white-space:nowrap}
.chip .path{font:600 13px/1 ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--fg);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.tip{margin:14px auto 0;display:flex;align-items:center;gap:8px;max-width:520px;font-size:13px;color:var(--muted);text-align:left}
.tip .i{flex:0 0 auto;display:grid;place-items:center;width:22px;height:22px;border-radius:50%;
  background:color-mix(in srgb,var(--accent) 16%,transparent);color:var(--accent);font-weight:800;font-size:12px}

/* ── Actions ────────────────────────────────────────── */
.actions{margin-top:30px;display:flex;flex-wrap:wrap;gap:12px;justify-content:center}
.btn{display:inline-flex;align-items:center;justify-content:center;gap:9px;height:48px;padding:0 22px;border-radius:14px;
  font-size:14px;font-weight:600;text-decoration:none;transition:transform .18s ease,box-shadow .18s ease,background .18s ease;cursor:pointer}
.btn:active{transform:translateY(1px)}
.btn-primary{background:var(--primary);color:#fff;box-shadow:0 10px 24px var(--shadow)}
.btn-primary:hover{transform:translateY(-2px);box-shadow:0 14px 30px var(--shadow)}
.btn-ghost{border:1px solid var(--border);background:var(--card);color:var(--fg);backdrop-filter:blur(8px)}
.btn-ghost:hover{background:color-mix(in srgb,var(--ring) 70%,transparent);transform:translateY(-2px)}
.quick{margin-top:26px;display:flex;align-items:center;justify-content:center;gap:8px;flex-wrap:wrap;font-size:13px;color:var(--muted)}
.quick a{text-decoration:none;font-weight:600;color:var(--fg);padding:4px 10px;border-radius:8px;transition:background .15s ease,color .15s ease}
.quick a:hover{background:var(--ring);color:var(--fg)}
.quick .sep{color:color-mix(in srgb,var(--muted) 50%,transparent)}

footer{position:relative;z-index:2;text-align:center;padding:22px 24px 26px;color:var(--muted);font-size:12px;letter-spacing:.02em}
footer .dot{margin:0 8px;opacity:.5}

@media (max-width:560px){
  header{padding:16px 20px}
  .scene{height:150px}
  .chip{width:100%;justify-content:center}
  .chip .label{display:none}
  .tip{text-align:center;justify-content:center}
}
@media (prefers-reduced-motion:reduce){*{animation:none!important}.hero>*{opacity:1;transform:none}}
</style>
</head>
<body>
<div class="ambient"><div class="blob a"></div><div class="blob b"></div><div class="blob c"></div></div>

<header>
  <a class="frame" href="` + p.link("/") + `"><span class="logo">` + monitorMarkSVG + `</span><span>Monitor<span class="brand-dot">.</span></span></a>
  <span class="code-pill">Error ` + code + `</span>
</header>

<main>
  <div class="wrap">
    <div class="hero">
      <span class="badge">` + badge + `</span>

      <div class="scene" aria-hidden="true">
        <div class="rings"><span></span><span></span><span></span></div>
        <div class="code">` + code + `</div>
      </div>

      <h1>` + title + `</h1>
      <p>` + message + `</p>

      <div class="chip">
        <span class="label">Requested path</span>
        <span class="path">` + path + `</span>
      </div>

      <div class="tip"><span class="i">i</span><span>` + tip + `</span></div>

      <div class="actions">
        ` + p.primaryAction() + `
        <a class="btn btn-ghost" href="` + p.link("/docs") + `">Read the docs</a>
      </div>

      <nav class="quick">
        <a href="` + p.link("/app") + `">Overview</a><span class="sep">·</span>
        <a href="` + p.link("/app") + `">Your monitors</a><span class="sep">·</span>
        <a href="` + p.link("/app/api-keys") + `">API keys</a><span class="sep">·</span>
        <a href="` + p.link("/docs") + `">Documentation</a>
      </nav>
    </div>
  </div>
</main>

<footer>Monitor<span class="brand-dot">.</span><span class="dot">·</span>error page<span class="dot">·</span>` + code + `</footer>
</body>
</html>`
}

const monitorMarkSVG = `<svg viewBox="0 0 64 64" role="img" aria-label="Monitor">` +
	`<path d="M13 45C13 19 32 13 50 24" fill="none" stroke="currentColor" stroke-width="7" stroke-linecap="round"/>` +
	`<circle cx="13" cy="45" r="7" fill="currentColor"/><circle cx="50" cy="24" r="7" fill="currentColor"/></svg>`

func (p errorPage) link(path string) string {
	return p.Origin + path
}

func (p errorPage) originHost() string {
	return strings.TrimPrefix(strings.TrimPrefix(p.Origin, "https://"), "http://")
}

func (p errorPage) primaryAction() string {
	if p.OnMonitor {
		return `<a class="btn btn-primary" href="` + p.link("/app") + `">Back to dashboard</a>`
	}
	return `<a class="btn btn-primary" href="` + p.link("/") + `">Go to ` + p.originHost() + `</a>`
}
