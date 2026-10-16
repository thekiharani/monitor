const W = 600;
const H = 150;
const PAD = 6;
const SVG_NS = "http://www.w3.org/2000/svg";
const MINUTE = 60 * 1000;
const DAY = 1440 * MINUTE;
const RANGES = {
  "5m": { span: 5 * MINUTE, live: true, hint: "Last 5 minutes, live" },
  "15m": { span: 15 * MINUTE, live: true, hint: "Last 15 minutes, live" },
  "30m": { span: 30 * MINUTE, live: true, hint: "Last 30 minutes, live" },
  "1h": { span: 60 * MINUTE, hint: "Last hour, minute averages" },
  "3h": { span: 180 * MINUTE, hint: "Last 3 hours, minute averages" },
  "6h": { span: 360 * MINUTE, hint: "Last 6 hours, 2-minute averages" },
  "12h": { span: 720 * MINUTE, hint: "Last 12 hours, 3-minute averages" },
  "24h": { span: 1440 * MINUTE, hint: "Last 24 hours, 6-minute averages" },
};
const LEVELS = ["ok", "warn", "crit"];
const SPARK_EVERY = 5000;
const SPARK_SAMPLES = 60;
const STALE_AFTER = 6000;

const $ = (id) => document.getElementById(id);
const state = {
  range: "5m",
  points: [],
  latest: null,
  host: null,
  hoverT: null,
  pinnedT: null,
  timeline: [],
  lastMessage: 0,
  sparks: new Map(),
  cards: new Map(),
  rows: new Map(),
  filter: "",
  favicon: "",
  issuesKey: "",
  view: "host",
  containerName: null,
  detail: null,
  logs: [],
  logsAfter: "",
  follow: true,
  timers: [],
  dashboardScroll: 0,
  tab: "overview",
  logView: "all",
  containerState: "all",
  project: "",
  servicesView: "all",
};

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function svgEl(name, attrs = {}) {
  const node = document.createElementNS(SVG_NS, name);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  return node;
}

function formatBytes(n) {
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(n >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
}

const formatRate = (n) => `${formatBytes(n)}/s`;
const whole = (n) => `${Math.round(n)}%`;
const percent = (used, total) => (total ? (100 * used) / total : 0);
const count = (n) => Math.round(n).toLocaleString();
const perSecond = (n) => `${n < 10 ? n.toFixed(1) : count(n)}/s`;
const pctOrDash = (n) => (n ? `${n.toFixed(1)}%` : "-");
const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;

function formatDuration(seconds) {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${Math.max(0, Math.floor(seconds))}s`;
}

function formatAgo(unixSeconds) {
  if (!unixSeconds) return "never";
  const seconds = Date.now() / 1000 - unixSeconds;
  return seconds < 90 ? "just now" : `${formatDuration(seconds)} ago`;
}

function clock(t, seconds) {
  return new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: seconds ? "2-digit" : undefined, hourCycle: "h23" });
}

function dayClock(t) {
  const d = new Date(t);
  const today = new Date().toDateString() === d.toDateString();
  return `${today ? "Today" : "Yesterday"} ${clock(t)}`;
}

function icon(level) {
  const svg = svgEl("svg", { class: "icon", viewBox: "0 0 16 16", "data-level": level, "aria-hidden": "true" });
  if (level === "warn") {
    svg.append(svgEl("path", { d: "M8 1.2 15.2 14.4H.8z", fill: "currentColor" }), svgEl("path", { class: "glyph", d: "M8 6.2v3.4M8 12v.1" }));
  } else if (level === "crit") {
    svg.append(
      svgEl("path", { d: "M5.1 1h5.8L15 5.1v5.8L10.9 15H5.1L1 10.9V5.1z", fill: "currentColor" }),
      svgEl("path", { class: "glyph", d: "M5.9 5.9l4.2 4.2M10.1 5.9l-4.2 4.2" }),
    );
  } else if (level === "off") {
    svg.append(svgEl("circle", { cx: 8, cy: 8, r: 6.2, fill: "none", stroke: "currentColor", "stroke-width": 1.6 }));
  } else {
    svg.append(svgEl("circle", { cx: 8, cy: 8, r: 7, fill: "currentColor" }), svgEl("path", { class: "glyph", d: "M4.9 8.2l2.1 2.1 4.1-4.5" }));
  }
  return svg;
}

function tag(level, text) {
  const node = el("span", "tag");
  node.dataset.level = level;
  node.append(icon(level), text);
  return node;
}

function meter(value, warn = 80, crit = 95, thick = false) {
  const wrap = el("div", thick ? "meter thick" : "meter");
  const fill = el("span");
  fill.style.width = `${Math.min(100, Math.max(0, value)).toFixed(1)}%`;
  fill.classList.toggle("warn", value >= warn && value < crit);
  fill.classList.toggle("crit", value >= crit);
  wrap.append(fill);
  return wrap;
}

function dl(entries, className = "rows") {
  const list = el("dl", className);
  for (const [label, value, ratio] of entries) {
    const row = el("div");
    const dd = el("dd", "", value);
    row.append(el("dt", "", label), dd);
    if (ratio !== undefined) row.append(meter(ratio));
    list.append(row);
  }
  return list;
}

function rows(target, entries) {
  $(target).replaceChildren(...dl(entries).children);
}

function itemList(items, title) {
  const list = el("div", "list");
  if (title) list.append(el("h4", "", title));
  for (const { name, value, level, hint, sub } of items) {
    const item = el("div", "item");
    if (hint) item.title = hint;
    const label = el("span", "name");
    if (level) label.append(icon(level));
    label.append(el("span", "", name));
    if (sub) label.append(el("span", "sub", sub));
    item.append(label, el("span", level === "crit" || level === "warn" ? "value bad" : "value", value));
    list.append(item);
  }
  return list;
}

function monotonePath(xs, ys) {
  const n = xs.length;
  if (n === 0) return "";
  if (n === 1) return `M${xs[0]},${ys[0]}`;
  const slopes = [];
  for (let i = 0; i < n - 1; i++) slopes.push((ys[i + 1] - ys[i]) / (xs[i + 1] - xs[i] || 1));
  const tangents = [slopes[0]];
  for (let i = 1; i < n - 1; i++) tangents.push(slopes[i - 1] * slopes[i] <= 0 ? 0 : (slopes[i - 1] + slopes[i]) / 2);
  tangents.push(slopes[n - 2]);
  for (let i = 0; i < n - 1; i++) {
    if (slopes[i] === 0) {
      tangents[i] = 0;
      tangents[i + 1] = 0;
      continue;
    }
    const a = tangents[i] / slopes[i];
    const b = tangents[i + 1] / slopes[i];
    const s = a * a + b * b;
    if (s > 9) {
      const k = 3 / Math.sqrt(s);
      tangents[i] = k * a * slopes[i];
      tangents[i + 1] = k * b * slopes[i];
    }
  }
  let d = `M${xs[0]},${ys[0]}`;
  for (let i = 0; i < n - 1; i++) {
    const h = (xs[i + 1] - xs[i]) / 3;
    d += ` C${xs[i] + h},${ys[i] + h * tangents[i]} ${xs[i + 1] - h},${ys[i + 1] - h * tangents[i + 1]} ${xs[i + 1]},${ys[i + 1]}`;
  }
  return d;
}

function niceMax(value, floor) {
  const v = Math.max(value, floor);
  const exp = 10 ** Math.floor(Math.log10(v));
  for (const step of [1, 2, 2.5, 5, 10]) {
    if (v <= step * exp) return step * exp;
  }
  return 10 * exp;
}

const pct = (v) => `${Math.round(v)}%`;
const CHARTS = [
  { id: "cpu", view: "host", series: [{ key: "cpu", cls: "s1" }], floor: 10, cap: 100, label: pct },
  { id: "mem", view: "host", series: [{ key: "mem", cls: "s1" }], floor: 10, cap: 100, label: pct },
  {
    id: "disk",
    view: "host",
    series: [
      { key: "readBps", cls: "s1" },
      { key: "writeBps", cls: "s2" },
    ],
    floor: 256 * 1024,
    label: formatRate,
  },
  {
    id: "net",
    view: "host",
    series: [
      { key: "rxBps", cls: "s1" },
      { key: "txBps", cls: "s2" },
    ],
    floor: 64 * 1024,
    label: formatRate,
  },
  {
    id: "psi",
    view: "host",
    series: [
      { key: "psiCpu", cls: "s1" },
      { key: "psiMem", cls: "s2" },
      { key: "psiIo", cls: "s3" },
    ],
    floor: 5,
    cap: 100,
    label: pct,
  },
  { id: "c-cpu", view: "container", series: [{ key: "cpu", cls: "s1" }], floor: 10, label: pct },
  { id: "c-mem", view: "container", series: [{ key: "mem", cls: "s1" }], floor: 64 * 1024 * 1024, label: formatBytes },
  {
    id: "c-disk",
    view: "container",
    series: [
      { key: "readBps", cls: "s1" },
      { key: "writeBps", cls: "s2" },
    ],
    floor: 256 * 1024,
    label: formatRate,
  },
  {
    id: "c-net",
    view: "container",
    series: [
      { key: "rxBps", cls: "s1" },
      { key: "txBps", cls: "s2" },
    ],
    floor: 64 * 1024,
    label: formatRate,
  },
];

const viewRoot = () => $(state.view === "host" ? "dashboard" : "container-view");

function pointOf(s) {
  return {
    t: s.time,
    cpu: s.cpu.total,
    mem: percent(s.mem.used, s.mem.total),
    psiCpu: s.pressure?.cpu ?? 0,
    psiMem: s.pressure?.memory ?? 0,
    psiIo: s.pressure?.io ?? 0,
    readBps: s.disk.readBps,
    writeBps: s.disk.writeBps,
    rxBps: s.net.reduce((t, i) => t + i.rxBps, 0),
    txBps: s.net.reduce((t, i) => t + i.txBps, 0),
  };
}

const span = () => RANGES[state.range].span;
const endTime = () => Math.max(state.latest?.time ?? 0, state.points.at(-1)?.t ?? 0);
const xOf = (t) => W - ((endTime() - t) / span()) * W;

function drawChart(chart) {
  const svg = $(`${chart.id}-chart`);
  const peak = Math.max(0, ...state.points.flatMap((p) => chart.series.map((s) => p[s.key])));
  const nice = niceMax(peak * 1.15, chart.floor);
  chart.top = chart.cap ? Math.min(chart.cap, nice) : nice;
  $(`${chart.id}-scale`).textContent = chart.label(chart.top);
  svg.replaceChildren();
  for (let i = 1; i < 4; i++) {
    const y = PAD + ((H - 2 * PAD) * i) / 4;
    svg.append(svgEl("line", { x1: 0, x2: W, y1: y, y2: y, class: "gridline" }));
  }
  svg.append(svgEl("line", { x1: 0, x2: W, y1: H - 0.5, y2: H - 0.5, class: "baseline" }));
  const xs = state.points.map((p) => xOf(p.t));
  for (const { key, cls } of chart.series) {
    if (xs.length < 2) continue;
    const ys = state.points.map((p) => yOf(chart, p[key]));
    const line = monotonePath(xs, ys);
    svg.append(svgEl("path", { d: `${line} L${xs.at(-1)},${H} L${xs[0]},${H} Z`, class: `area ${cls}` }));
    svg.append(svgEl("path", { d: line, class: `line ${cls}` }));
  }
  const axis = $(`${chart.id}-axis`);
  const end = endTime();
  const ticks = [3, 2, 1].map((k) => clock(end - (span() * k) / 3, span() <= 15 * MINUTE));
  axis.replaceChildren(...[...ticks, "now"].map((t) => el("span", "", t)));
}

const yOf = (chart, v) => H - PAD - (Math.min(v, chart.top) / chart.top) * (H - 2 * PAD);

function renderCharts() {
  for (const chart of CHARTS) {
    if (chart.view !== state.view || (chart.id === "psi" && !state.latest?.pressure)) continue;
    drawChart(chart);
  }
  renderCursor();
}

function nearestIndex(t) {
  const pts = state.points;
  if (!pts.length) return -1;
  let lo = 0;
  let hi = pts.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (pts[mid].t < t) lo = mid + 1;
    else hi = mid;
  }
  if (lo > 0 && t - pts[lo - 1].t < pts[lo].t - t) lo--;
  return lo;
}

function cursorPoint() {
  const t = state.hoverT ?? state.pinnedT;
  if (t === null) return null;
  const i = nearestIndex(t);
  return i < 0 ? null : state.points[i];
}

function renderCursor() {
  const p = cursorPoint();
  const pinned = p !== null && state.hoverT === null;
  for (const plot of viewRoot().querySelectorAll(".plot")) {
    const chart = CHARTS.find((c) => c.id === plot.dataset.chart);
    let cursor = plot.querySelector(".cursor");
    if (!p || !chart?.top) {
      cursor?.remove();
      plot.querySelectorAll(".dot").forEach((d) => d.remove());
      continue;
    }
    if (!cursor) cursor = plot.appendChild(el("div", "cursor"));
    const left = `${(xOf(p.t) / W) * 100}%`;
    cursor.style.left = left;
    cursor.classList.toggle("pinned", pinned);
    const dots = plot.querySelectorAll(".dot");
    chart.series.forEach((s, i) => {
      const dot = dots[i] ?? plot.appendChild(el("span", `dot ${s.cls}`));
      dot.style.left = left;
      dot.style.top = `${(yOf(chart, p[s.key]) / H) * 100}%`;
    });
  }
  renderReadouts(p);
  renderRibbon();
}

function renderReadouts(p) {
  const s = state.latest;
  if (!s) return;
  const when = p ? `at ${clock(p.t, span() <= 30 * MINUTE)}` : "";
  viewRoot()
    .querySelectorAll(".when")
    .forEach((n) => (n.textContent = when));
  if (state.view === "container") {
    const live = currentContainer();
    const point = p ?? (live && containerPoint(live, s.time));
    if (!point) return;
    $("c-cpu-value").textContent = point.cpu < 10 ? point.cpu.toFixed(1) : Math.round(point.cpu);
    $("c-mem-value").textContent = formatBytes(point.mem);
    $("c-mem-limit").textContent = live?.memLimit ? ` of ${formatBytes(live.memLimit)}` : "";
    $("c-disk-read").textContent = formatRate(point.readBps);
    $("c-disk-write").textContent = formatRate(point.writeBps);
    $("c-net-rx").textContent = formatRate(point.rxBps);
    $("c-net-tx").textContent = formatRate(point.txBps);
    return;
  }
  const point = p ?? pointOf(s);
  $("cpu-value").textContent = Math.round(point.cpu);
  $("mem-value").textContent = Math.round(point.mem);
  $("disk-read").textContent = formatRate(point.readBps);
  $("disk-write").textContent = formatRate(point.writeBps);
  $("net-rx").textContent = formatRate(point.rxBps);
  $("net-tx").textContent = formatRate(point.txBps);
  $("psi-cpu").textContent = `${point.psiCpu.toFixed(1)}%`;
  $("psi-mem").textContent = `${point.psiMem.toFixed(1)}%`;
  $("psi-io").textContent = `${point.psiIo.toFixed(1)}%`;
}

function bindPlot(plot) {
  const timeAt = (e) => {
    const r = plot.getBoundingClientRect();
    return endTime() - (1 - (e.clientX - r.left) / r.width) * span();
  };
  plot.addEventListener("pointermove", (e) => {
    state.hoverT = timeAt(e);
    renderCursor();
  });
  plot.addEventListener("pointerleave", () => {
    state.hoverT = null;
    renderCursor();
  });
  plot.addEventListener("click", (e) => {
    const p = state.points[nearestIndex(timeAt(e))];
    state.pinnedT = p && state.pinnedT !== p.t ? p.t : null;
    state.hoverT = null;
    renderCursor();
  });
  plot.addEventListener("keydown", (e) => {
    if (!state.points.length) return;
    const current = cursorPoint();
    let i = current ? nearestIndex(current.t) : state.points.length - 1;
    const step = e.shiftKey ? 10 : 1;
    if (e.key === "ArrowLeft") i = Math.max(0, i - step);
    else if (e.key === "ArrowRight") i = Math.min(state.points.length - 1, i + step);
    else if (e.key === "Home") i = 0;
    else if (e.key === "End") i = state.points.length - 1;
    else if (e.key === "Escape") i = -1;
    else return;
    e.preventDefault();
    state.pinnedT = i < 0 ? null : state.points[i].t;
    renderCursor();
  });
}

function renderRibbon() {
  const svg = $("ribbon-svg");
  const end = Date.now();
  const start = end - DAY;
  const x = (t) => ((t - start) / DAY) * 1000;
  const cells = state.timeline.filter((p) => p.t > start);
  const gaps = cells.slice(1).map((p, i) => p.t - cells[i].t);
  const bucket = gaps.length ? gaps.sort((a, b) => a - b)[gaps.length >> 1] : MINUTE;
  const width = Math.max(1, (bucket / DAY) * 1000);
  const nodes = cells.map((p) =>
    svgEl("rect", { x: x(p.t) - width, y: 0, width: Math.max(0.6, width - 0.8), height: 20, class: `cell l${p.level ?? 0}` }),
  );
  const windowStart = x(end - span());
  nodes.push(svgEl("rect", { x: Math.max(0, windowStart), y: 0.75, width: 1000 - Math.max(0, windowStart) - 0.75, height: 18.5, rx: 2, class: "window" }));
  const p = cursorPoint();
  if (p) nodes.push(svgEl("line", { x1: x(p.t), x2: x(p.t), y1: 0, y2: 20, class: "mark" }));
  svg.replaceChildren(...nodes);
}

function bindRibbon() {
  const track = $("ribbon-track");
  const tip = $("ribbon-tip");
  const timeAt = (e) => {
    const r = track.getBoundingClientRect();
    const frac = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
    return { t: Date.now() - (1 - frac) * DAY, frac };
  };
  const nearest = (t) => {
    let best = null;
    for (const p of state.timeline) if (!best || Math.abs(p.t - t) < Math.abs(best.t - t)) best = p;
    return best && Math.abs(best.t - t) <= 10 * MINUTE ? best : null;
  };
  track.addEventListener("pointermove", (e) => {
    const { t, frac } = timeAt(e);
    const p = nearest(t);
    tip.replaceChildren(el("b", "", dayClock(p?.t ?? t)), p ? (p.issue ?? "All good") : "No readings");
    tip.style.left = `${Math.min(88, Math.max(12, frac * 100))}%`;
    tip.hidden = false;
  });
  track.addEventListener("pointerleave", () => (tip.hidden = true));
  track.addEventListener("click", (e) => {
    const { t } = timeAt(e);
    const age = Date.now() - t;
    const fits = Object.keys(RANGES).find((r) => RANGES[r].span >= age * 1.05) ?? "24h";
    state.pinnedT = t;
    if (RANGES[fits].span > span()) selectRange(fits);
    else renderCursor();
  });
}

async function loadTimeline() {
  try {
    state.timeline = await getJSON("api/history?range=24h");
    renderRibbon();
  } catch {
    state.timeline = [];
  }
}

function renderVerdict(s) {
  const v = s.verdict;
  const box = $("verdict");
  box.dataset.level = v.level;
  const title = { ok: "All good", warn: "Needs attention", crit: "Something's wrong" }[v.level];
  $("verdict-title").textContent = title;
  $("verdict-icon").replaceChildren(icon(v.level));
  const root = (s.filesystems ?? []).find((f) => f.mount === "/") ?? s.filesystems?.[0];
  const services = serviceInstances(s);
  const down = services.filter(([, , x]) => !x.up).length;
  const running = s.containers?.filter((c) => c.state === "running").length;
  $("verdict-summary").textContent = [
    `CPU ${whole(s.cpu.total)}`,
    `memory ${whole(percent(s.mem.used, s.mem.total))}`,
    root && `disk ${whole(percent(root.used, root.total))} full`,
    s.containers && `${running} of ${s.containers.length} containers running`,
    services.length && `${plural(services.length, "service", "services")}${down ? `, ${down} down` : " up"}`,
  ]
    .filter(Boolean)
    .join(" · ");
  const issuesKey = JSON.stringify(v.reasons ?? []);
  if (issuesKey !== state.issuesKey) {
    state.issuesKey = issuesKey;
    renderIssues(v.reasons ?? []);
  }
  const host = state.host?.hostname || "Server";
  document.title = `${title} · ${host}`;
  setFavicon(v.level);
}

function renderIssues(reasons) {
  $("issues").replaceChildren(
    ...reasons.map((r) => {
      const li = el("li");
      const button = el("button");
      button.type = "button";
      button.append(icon(r.level), el("span", "text", r.text), el("span", "go", "Show"));
      button.addEventListener("click", () => reveal(r.ref));
      li.append(button);
      return li;
    }),
  );
}

function reveal(ref) {
  if (ref.startsWith("container:")) {
    location.hash = containerHref(ref.slice("container:".length));
    return;
  }
  const target = [...document.querySelectorAll("[data-ref]")].find((n) => n.dataset.ref === ref);
  if (!target) return;
  const smooth = !matchMedia("(prefers-reduced-motion: reduce)").matches;
  target.scrollIntoView({ behavior: smooth ? "smooth" : "auto", block: "center" });
  target.classList.add("flash");
  setTimeout(() => target.classList.remove("flash"), 1800);
}

function setFavicon(level) {
  if (state.favicon === level) return;
  state.favicon = level;
  const color = { ok: "#0ca30c", warn: "#fab219", crit: "#d03b3b" }[level];
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="6.5" fill="${color}"/></svg>`;
  $("favicon").href = `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

function renderCPU(s) {
  $("cpu-user").textContent = whole(s.cpu.user);
  $("cpu-system").textContent = whole(s.cpu.system);
  $("cpu-iowait").textContent = whole(s.cpu.iowait);
  $("cpu-steal").textContent = whole(s.cpu.steal);
  const wrap = $("cores");
  wrap.style.setProperty("--cores", s.cpu.cores.length);
  if (wrap.children.length !== s.cpu.cores.length) {
    wrap.replaceChildren(
      ...s.cpu.cores.map(() => {
        const core = el("div", "core");
        core.append(el("span"));
        return core;
      }),
    );
  }
  s.cpu.cores.forEach((value, i) => {
    const core = wrap.children[i];
    core.title = `Core ${i}: ${whole(value)}`;
    core.firstChild.style.height = `${Math.min(100, value).toFixed(1)}%`;
  });
}

function renderMemory(s) {
  $("mem-used").textContent = `${formatBytes(s.mem.used)} / ${formatBytes(s.mem.total)}`;
  $("mem-cached").textContent = formatBytes(s.mem.cached);
  $("mem-swap").textContent = s.mem.swapTotal ? `${formatBytes(s.mem.swapUsed)} / ${formatBytes(s.mem.swapTotal)}` : "off";
}

function renderIO(s) {
  const ms = (v, ops) => (ops > 0 ? `${v < 10 ? v.toFixed(1) : Math.round(v)} ms` : "idle");
  $("disk-latency").textContent = `${ms(s.disk.readLatency, s.disk.readIops)} · ${ms(s.disk.writeLatency, s.disk.writeIops)}`;
  $("disk-queue").textContent = s.disk.queue.toFixed(s.disk.queue < 10 ? 1 : 0);
  $("disk-iops").textContent = `${Math.round(s.disk.readIops)} · ${Math.round(s.disk.writeIops)} /s`;
  $("disk-busy").textContent = whole(s.disk.busy);
  const busiest = [...s.net].sort((a, b) => b.rxBps + b.txBps - (a.rxBps + a.txBps));
  $("ifaces").replaceChildren(...itemList(busiest.map((i) => ({ name: i.name, value: `in ${formatRate(i.rxBps)} · out ${formatRate(i.txBps)}` }))).children);
}

function renderFilesystems(s) {
  $("pressure-card").hidden = !s.pressure;
  $("filesystems").replaceChildren(
    ...s.filesystems.map((fs) => {
      const used = percent(fs.used, fs.total);
      const item = el("div");
      const head = el("div", "fs-head");
      const title = el("strong", "", fs.mount);
      const flags = el("span", "", `${fs.type} · ${fs.device}`);
      head.append(title, flags);
      if (fs.readOnly) head.append(tag("crit", "read-only"));
      if (fs.errors) head.append(tag("warn", plural(fs.errors, "error", "errors")));
      const foot = el("p", "fs-foot", `${formatBytes(fs.used)} of ${formatBytes(fs.total)} · ${whole(used)} full · ${whole(percent(fs.inodesUsed, fs.inodesTotal))} of inodes`);
      item.append(head, meter(used, 85, 95, true), foot);
      return item;
    }),
  );
}

const HINTS = {
  unreachable: "Nothing answered. Check the host and port, and that the monitor can reach the service's network.",
  "not responding": "The connection timed out. Check firewalls, and whether the service is overloaded.",
  "rejecting the login": "Check the username and password in the URL, and the grants listed in the README.",
  "presenting an invalid certificate": "The TLS certificate is expired, self-signed, or issued for another name.",
};

const ratio = (used, limit) => (limit ? percent(used, limit) : undefined);
const ofLimit = (used, limit, format = count) => (limit ? `${format(used)} / ${format(limit)}` : format(used));
const largest = (dbs) => ({ title: "Largest databases", items: (dbs ?? []).map((d) => ({ name: d.name, value: formatBytes(d.size) })) });

const SERVICES = [
  {
    key: "postgres",
    title: () => "Postgres",
    meta: (x) => `${x.version.split(" ")[0]} · up ${formatDuration(x.uptime)}`,
    spark: { label: "Transactions", value: (x) => x.tps },
    stats: (x) => [
      ["Connections", `${x.connections} / ${x.maxConnections}`, ratio(x.connections, x.maxConnections)],
      ["Running now", `${x.active}`],
      ["Cache hits", pctOrDash(x.cacheHit)],
      ["Longest query", x.longestQuery ? formatDuration(x.longestQuery) : "none"],
    ],
    list: (x) => largest(x.databases),
  },
  {
    key: "mysql",
    title: (x) => x.flavor || "MySQL",
    meta: (x) => `${x.version} · up ${formatDuration(x.uptime)}`,
    spark: { label: "Queries", value: (x) => x.qps },
    stats: (x) => [
      ["Connections", `${x.connections} / ${x.maxConnections}`, ratio(x.connections, x.maxConnections)],
      ["Running now", `${x.running}`],
      ["Buffer pool hits", pctOrDash(x.bufferPoolHit)],
      ["Slow queries", count(x.slowQueries)],
      ["Longest query", x.longestQuery ? formatDuration(x.longestQuery) : "none"],
      ...(x.replica
        ? [["Replication", x.replica.ioRunning && x.replica.sqlRunning ? (x.replica.lag === null ? "running" : `${formatDuration(x.replica.lag)} behind`) : "stopped"]]
        : []),
    ],
    list: (x) => largest(x.databases),
  },
  {
    key: "mongo",
    title: () => "MongoDB",
    meta: (x) => `${x.version} · up ${formatDuration(x.uptime)}`,
    spark: { label: "Operations", value: (x) => x.opsPerSec },
    stats: (x) => [
      ["Connections", `${count(x.connections)} / ${count(x.maxConnections)}`, ratio(x.connections, x.maxConnections)],
      ["Memory", formatBytes(x.memory)],
      ["Role", x.replicaSet ? `${x.role} · ${x.replicaSet}` : x.role],
      ...(x.lag !== null ? [["Replication", `${formatDuration(x.lag)} behind`]] : []),
    ],
    list: (x) => largest(x.databases),
  },
  {
    key: "redis",
    title: () => "Redis",
    meta: (x) => `${x.version} · up ${formatDuration(x.uptime)}`,
    spark: { label: "Commands", value: (x) => x.opsPerSec },
    stats: (x) => [
      ["Memory", ofLimit(x.memory, x.maxMemory, formatBytes), ratio(x.memory, x.maxMemory)],
      ["Clients", count(x.clients)],
      ["Cache hits", pctOrDash(x.hitRate)],
      ["Keys", count(x.keys)],
      ["Evicted", count(x.evicted)],
    ],
  },
  {
    key: "memcached",
    title: () => "Memcached",
    meta: (x) => `${x.version} · up ${formatDuration(x.uptime)}`,
    spark: { label: "Commands", value: (x) => x.opsPerSec },
    stats: (x) => [
      ["Memory", ofLimit(x.memory, x.maxMemory, formatBytes), ratio(x.memory, x.maxMemory)],
      ["Connections", ofLimit(x.connections, x.maxConnections), ratio(x.connections, x.maxConnections)],
      ["Cache hits", pctOrDash(x.hitRate)],
      ["Items", count(x.items)],
      ["Evicted", count(x.evicted)],
    ],
  },
  {
    key: "nginx",
    title: () => "Nginx",
    meta: () => "stub_status",
    spark: { label: "Requests", value: (x) => x.requestsPerSec },
    stats: (x) => [
      ["Connections", count(x.active)],
      ["Reading", count(x.reading)],
      ["Writing", count(x.writing)],
      ["Idle", count(x.waiting)],
      ["Dropped", count(x.dropped)],
    ],
  },
  {
    key: "rabbitmq",
    title: () => "RabbitMQ",
    meta: (x) => `${x.version}${x.cluster ? ` · ${x.cluster}` : ""}`,
    spark: { label: "Published", value: (x) => x.publishRate },
    stats: (x) => [
      ["Messages", count(x.messages)],
      ["Ready", count(x.ready)],
      ["Unacknowledged", count(x.unacked)],
      ["Delivered", perSecond(x.deliverRate)],
      ["Connections", count(x.connections)],
      ["Queues", count(x.queues)],
      ["Consumers", count(x.consumers)],
    ],
    list: (x) => ({
      title: "Nodes",
      items: (x.nodes ?? []).map((n) => {
        const alarm = !n.running ? "down" : n.mem_alarm ? "memory alarm" : n.disk_free_alarm ? "disk alarm" : "";
        return { name: n.name, value: alarm || `${formatBytes(n.mem_used)} memory`, level: alarm ? "crit" : "ok" };
      }),
    }),
  },
];

function serviceInstances(s) {
  return SERVICES.flatMap((kind) => (s[kind.key] ?? []).map((x) => [`${kind.key}:${x.name}`, kind, x]));
}

function recordSpark(key, value, now) {
  const samples = state.sparks.get(key) ?? [];
  if (!samples.length || now - samples.at(-1).t >= SPARK_EVERY) {
    samples.push({ t: now, v: value });
    if (samples.length > SPARK_SAMPLES) samples.shift();
    state.sparks.set(key, samples);
  }
  return samples;
}

function sparkline(samples) {
  const svg = svgEl("svg", { viewBox: "0 0 200 40", preserveAspectRatio: "none", "aria-hidden": "true" });
  if (samples.length < 2) return svg;
  const top = Math.max(...samples.map((p) => p.v)) * 1.15 || 1;
  const step = 200 / Math.max(samples.length - 1, 12);
  const xs = samples.map((_, i) => 200 - (samples.length - 1 - i) * step);
  const ys = samples.map((p) => 38 - (p.v / top) * 34);
  const line = monotonePath(xs, ys);
  svg.append(svgEl("path", { d: `${line} L${xs.at(-1)},40 L${xs[0]},40 Z`, class: "area" }), svgEl("path", { d: line, class: "line" }));
  return svg;
}

function card(key) {
  let node = state.cards.get(key);
  if (!node) {
    node = el("article", "card svc");
    node.dataset.ref = key;
    state.cards.set(key, node);
  }
  return node;
}

function head(title, name, meta, status) {
  const header = el("header", "svc-head");
  const left = el("div");
  const h = el("h3", "", title);
  if (name) h.append(el("small", "", name));
  left.append(h);
  if (meta) left.append(el("p", "svc-meta", meta));
  header.append(left, status);
  return header;
}

function serviceCard(key, kind, x, now) {
  const node = card(key);
  node.dataset.problem = x.up ? "" : "1";
  const parts = [head(kind.title(x), x.name, x.up ? kind.meta(x) : "", x.up ? tag("ok", "up") : tag("crit", x.error || "down"))];
  if (!x.up) {
    state.sparks.delete(key);
    parts.push(el("p", "hint", HINTS[x.error] ?? "Check the URL, credentials, and network access."));
  } else {
    const value = kind.spark.value(x);
    const spark = el("div", "spark");
    const label = el("div");
    label.append(el("b", "", perSecond(value)), el("span", "", `${kind.spark.label} per second, last 5 minutes`));
    spark.append(label, sparkline(recordSpark(key, value, now)));
    parts.push(spark, dl(kind.stats(x), "stats"));
    const list = kind.list?.(x);
    if (list?.items.length) parts.push(itemList(list.items, list.title));
  }
  node.replaceChildren(...parts);
  return node;
}

function checksCard(checks) {
  const node = card("checks");
  const down = checks.filter((c) => !c.up).length;
  node.dataset.problem = down ? "1" : "";
  const status = down ? tag("crit", `${down} failing`) : tag("ok", "all passing");
  const latency = (ms) => `${ms < 10 ? ms.toFixed(1) : Math.round(ms)} ms`;
  const expiry = (c) => {
    if (!c.certExpires) return "";
    const days = (c.certExpires - Date.now() / 1000) / 86400;
    return ` · cert ${Math.floor(days)}d`;
  };
  const certLevel = (c) => {
    const days = (c.certExpires - Date.now() / 1000) / 86400;
    return !c.certExpires || days > 21 ? "ok" : days > 7 ? "warn" : "crit";
  };
  const sorted = [...checks].sort((a, b) => a.up - b.up || a.name.localeCompare(b.name));
  node.replaceChildren(
    head("Checks", "", `${plural(checks.length, "endpoint", "endpoints")} every 15 seconds`, status),
    itemList(
      sorted.map((c) => ({
        name: c.name,
        value: c.up ? latency(c.latency) + expiry(c) : c.error,
        level: c.up ? certLevel(c) : "crit",
        hint: c.target,
      })),
    ),
  );
  return node;
}

function systemdCard(sd) {
  const node = card("systemd");
  node.dataset.problem = !sd.up || sd.failed.length || sd.watched.some((u) => u.active !== "active") ? "1" : "";
  if (!sd.up) {
    node.replaceChildren(head("Services", "systemd", "", tag("crit", sd.error || "unreachable")), el("p", "hint", "Check that compose.systemd.yml is in COMPOSE_FILE and the D-Bus socket is mounted."));
    return node;
  }
  const failed = new Set(sd.failed.map((u) => u.name));
  const units = [...sd.failed, ...sd.watched.filter((u) => !failed.has(u.name))];
  const status = sd.failed.length ? tag("crit", `${sd.failed.length} failed`) : tag("ok", "none failed");
  const parts = [head("Services", "systemd", `${sd.running} of ${sd.services} loaded services running`, status)];
  if (units.length) {
    parts.push(
      itemList(
        units.map((u) => ({
          name: u.name,
          value: u.active === "active" ? u.sub : `${u.active} (${u.sub})`,
          level: u.active === "active" ? "ok" : "crit",
          hint: u.description,
        })),
      ),
    );
  } else {
    parts.push(el("p", "note", "No failed units. Add names to SYSTEMD_UNITS to watch specific services."));
  }
  node.replaceChildren(...parts);
  return node;
}

function renderServices(s) {
  const now = Date.now();
  const instances = serviceInstances(s);
  const cards = instances.map(([key, kind, x]) => serviceCard(key, kind, x, now));
  if (s.checks?.length) cards.push(checksCard(s.checks));
  if (s.systemd) cards.push(systemdCard(s.systemd));
  const keep = new Set(cards);
  for (const [key, node] of state.cards) if (!keep.has(node)) state.cards.delete(key);
  const grid = $("services");
  cards.forEach((node, i) => {
    if (grid.children[i] !== node) grid.insertBefore(node, grid.children[i] ?? null);
  });
  while (grid.children.length > cards.length) grid.lastChild.remove();
  $("services-section").hidden = cards.length === 0;
  const q = $("services-filter").value.trim().toLowerCase();
  let shown = 0;
  for (const node of cards) {
    node.hidden = (state.servicesView === "problems" && !node.dataset.problem) || (q && !`${node.dataset.ref} ${node.textContent}`.toLowerCase().includes(q));
    if (!node.hidden) shown++;
  }
  $("services-empty").hidden = shown > 0;
  $("services-empty").textContent = state.servicesView === "problems" && !q ? "Every service is up." : "No services match.";
  const down = instances.filter(([, , x]) => !x.up).length;
  const failing = s.checks?.filter((c) => !c.up).length ?? 0;
  $("services-summary").textContent = [
    instances.length && `${plural(instances.length, "service", "services")}${down ? `, ${down} down` : ""}`,
    s.checks?.length && `${plural(s.checks.length, "check", "checks")}${failing ? `, ${failing} failing` : ""}`,
    s.systemd?.up && `${plural(s.systemd.failed.length, "failed unit", "failed units")}`,
  ]
    .filter(Boolean)
    .join(" · ");
}

function containerState(c) {
  if (c.health === "unhealthy") return ["unhealthy", "crit"];
  if (c.state === "restarting") return ["restarting", "crit"];
  if (c.state === "exited") return c.exitCode ? [`exited (${c.exitCode})`, "warn"] : ["stopped", "off"];
  if (c.health === "starting") return ["starting", "off"];
  if (c.health === "healthy") return ["healthy", "ok"];
  return [c.state, c.state === "running" ? "ok" : "off"];
}

function containerRow(c) {
  let tr = state.rows.get(c.name);
  if (tr) return tr;
  tr = el("tr", "link-row");
  const name = el("td", "name");
  const link = el("a", "", c.name);
  link.href = containerHref(c.name);
  name.append(link, el("small"));
  tr.append(name, el("td"), el("td", "num"), el("td", "num"), el("td", "num"), el("td", "num"), el("td"));
  tr.addEventListener("click", (e) => {
    if (e.target.closest("a") || getSelection().toString()) return;
    location.hash = containerHref(c.name);
  });
  state.rows.set(c.name, tr);
  return tr;
}

function setText(node, text) {
  if (node.textContent !== text) node.textContent = text;
}

function renderProjects(containers) {
  const projects = [...new Set(containers.map((c) => c.project).filter(Boolean))].sort();
  const select = $("containers-project");
  select.hidden = projects.length === 0;
  const key = projects.join(",");
  if (select.dataset.key === key) return;
  select.dataset.key = key;
  const all = el("option", "", "All projects");
  all.value = "";
  select.replaceChildren(
    all,
    ...projects.map((p) => {
      const option = el("option", "", p);
      option.value = p;
      return option;
    }),
  );
  if (!projects.includes(state.project)) state.project = "";
  select.value = state.project;
}

function renderContainers(s) {
  $("containers-section").hidden = !s.containers;
  if (!s.containers) return;
  const rank = { crit: 0, warn: 1, ok: 2, off: 3 };
  const filter = state.filter.toLowerCase();
  const problem = (c) => ["crit", "warn"].includes(containerState(c)[1]);
  const byState = {
    all: () => true,
    running: (c) => c.state === "running",
    stopped: (c) => c.state !== "running",
    problems: problem,
  };
  const inProject = s.containers.filter((c) => !state.project || c.project === state.project);
  for (const button of $("containers-states").querySelectorAll("button")) {
    button.lastChild.textContent = inProject.filter(byState[button.dataset.state]).length;
  }
  renderProjects(s.containers);
  const visible = inProject.filter(byState[state.containerState]).filter((c) => !filter || `${c.name} ${c.image} ${c.project ?? ""} ${c.service ?? ""}`.toLowerCase().includes(filter));
  const sorted = visible.sort((a, b) => rank[containerState(a)[1]] - rank[containerState(b)[1]] || a.name.localeCompare(b.name));
  const running = s.containers.filter((c) => c.state === "running").length;
  $("containers-summary").textContent = `${running} running · ${s.containers.length - running} stopped`;
  $("containers-empty").hidden = sorted.length > 0;
  $("containers-empty").textContent = state.containerState === "problems" && !filter ? "No container has a problem." : "No containers match.";
  const names = new Set(s.containers.map((c) => c.name));
  for (const name of state.rows.keys()) if (!names.has(name)) state.rows.delete(name);
  const body = $("containers");
  sorted.forEach((c, i) => {
    const tr = containerRow(c);
    const [nameCell, stateCell, cpu, memory, read, write, since] = tr.children;
    setText(nameCell.lastChild, c.image);
    const [label, level] = containerState(c);
    if (stateCell.dataset.key !== label + level) {
      stateCell.dataset.key = label + level;
      stateCell.replaceChildren(tag(level, label));
    }
    const live = c.state === "running";
    setText(cpu, live ? `${c.cpu.toFixed(1)}%` : "-");
    setText(memory, c.memLimit ? `${formatBytes(c.memory)} / ${formatBytes(c.memLimit)}` : c.memory ? formatBytes(c.memory) : "-");
    setText(read, live ? formatRate(c.readBps) : "-");
    setText(write, live ? formatRate(c.writeBps) : "-");
    setText(since, live ? c.status.replace(/^Up /, "").replace(/ \(.*\)$/, "") : c.status.replace(/^Exited \(-?\d+\) /, "stopped "));
    if (body.children[i] !== tr) body.insertBefore(tr, body.children[i] ?? null);
  });
  while (body.children.length > sorted.length) body.lastChild.remove();
}

function renderProcesses(s) {
  const list = (id, items, value) =>
    $(id).replaceChildren(
      ...items.map((p) => {
        const li = el("li");
        li.title = `PID ${p.pid}`;
        li.append(el("span", "", p.name), el("span", "", value(p)));
        return li;
      }),
    );
  list("procs-cpu", s.processes.byCpu ?? [], (p) => `${p.cpu.toFixed(1)}%`);
  list("procs-mem", s.processes.byMemory ?? [], (p) => formatBytes(p.rss));
}

function renderUpkeep(s) {
  const h = s.health;
  const updates = h.updates ? (h.updates.pending ? `${h.updates.pending} waiting${h.updates.security ? ` · ${h.updates.security} security` : ""}` : "up to date") : "-";
  rows("upkeep", [
    ["Updates", updates],
    ["Auto-updates ran", formatAgo(h.lastAutoUpdate)],
    ["Reboot needed", h.rebootRequired ? "yes" : "no"],
    ["Drive check", h.smart ? formatAgo(h.smart.checkedAt) : "not set up"],
  ]);
  $("drives").replaceChildren(
    ...(h.smart?.disks ?? []).map((d) => {
      const box = el("div", "drive");
      const top = el("div", "drive-head");
      const failing = d.passed === false;
      top.append(el("strong", "", d.model), failing ? tag("crit", "failing") : tag("ok", "healthy"));
      box.append(top);
      const bad = d.reallocated + d.pending + d.uncorrectable;
      const facts = [
        ["Temperature", d.tempC ? `${d.tempC}°C` : "-"],
        ["Written", d.writtenBytes ? formatBytes(d.writtenBytes) : "-"],
        ["Powered on", d.powerOnHours ? formatDuration(d.powerOnHours * 3600) : "-"],
        ["Power cuts", `${d.unsafeShutdowns}`],
        ["Bad sectors", `${bad}`],
      ];
      if (d.lifeLeft !== null && d.lifeLeft !== undefined) facts.unshift(["Life left", `${d.lifeLeft}%`, 100 - d.lifeLeft]);
      box.append(dl(facts));
      return box;
    }),
  );
}

function render() {
  const s = state.latest;
  if (!s) return;
  if (state.view === "container") renderContainerLive(s);
  $("uptime").textContent = formatDuration(s.uptime);
  $("load").textContent = s.load.map((v) => v.toFixed(2)).join("  ");
  $("temp").textContent = s.tempC ? `${Math.round(s.tempC)}°C` : "-";
  renderVerdict(s);
  renderCPU(s);
  renderMemory(s);
  renderIO(s);
  renderFilesystems(s);
  renderServices(s);
  renderContainers(s);
  renderSections(s);
  renderProcesses(s);
  renderUpkeep(s);
  renderCharts();
  if (state.view === "container") document.title = `${state.containerName} · ${state.host?.hostname || "Server"}`;
}

function setStatus(kind, text) {
  const status = $("status");
  status.classList.toggle("live", kind === "live");
  status.classList.toggle("stale", kind === "stale");
  $("status-text").textContent = text;
  document.body.classList.toggle("stale", kind === "stale");
}

async function getJSON(path) {
  const res = await fetch(path, { cache: "no-store" });
  if (!res.ok) throw new Error(res.statusText);
  return res.json();
}

async function loadRange() {
  const requested = `${state.view}:${state.containerName}:${state.range}`;
  const url = state.view === "container" ? `api/containers/${encodeURIComponent(state.containerName)}/history?range=${state.range}` : `api/history?range=${state.range}`;
  try {
    const fetched = await getJSON(url);
    if (requested !== `${state.view}:${state.containerName}:${state.range}`) return;
    state.points = fetched;
  } catch {
    state.points = [];
  }
  if (state.pinnedT !== null && state.pinnedT < endTime() - span()) state.pinnedT = null;
  renderCharts();
}

let rangeTimer = null;

function selectRange(next) {
  state.range = next;
  document.querySelectorAll(".range button").forEach((b) => b.setAttribute("aria-selected", String(b.dataset.range === next)));
  $("range-hint").textContent = `${RANGES[next].hint}. Hover a chart to read any moment; click to pin it.`;
  $("c-range-hint").textContent = `${RANGES[next].hint}, kept since the monitor started. Hover to read any moment; click to pin it.`;
  clearInterval(rangeTimer);
  rangeTimer = setInterval(loadRange, RANGES[next].live ? 30000 : 60000);
  loadRange();
}

async function loadHost() {
  try {
    const host = await getJSON("api/host");
    state.host = host;
    $("hostname").textContent = host.hostname || "Server";
    $("bar-host").textContent = host.hostname || "Server";
    $("host-meta").textContent = [host.os, `kernel ${host.kernel}`, host.cpuModel, `${host.cores} cores`].filter(Boolean).join(" · ");
    $("foot").textContent = `Monitoring ${host.hostname} · readings every second · monitor ${host.version}`;
  } catch {
    $("host-meta").textContent = "";
  }
}

function connect() {
  const source = new EventSource("api/stream");
  source.onopen = () => setStatus("live", "Live");
  source.onmessage = (e) => {
    state.latest = JSON.parse(e.data);
    state.lastMessage = Date.now();
    if (RANGES[state.range].live) {
      const point = state.view === "host" ? pointOf(state.latest) : liveContainerPoint();
      if (point) state.points.push(point);
      const cutoff = state.latest.time - span();
      while (state.points.length && state.points[0].t < cutoff) state.points.shift();
    }
    const last = state.timeline.at(-1);
    const level = LEVELS.indexOf(state.latest.verdict.level);
    if (!last || state.latest.time - last.t >= MINUTE) {
      state.timeline.push({ t: state.latest.time, level, issue: state.latest.verdict.reasons.find((r) => r.level === state.latest.verdict.level)?.text });
    } else if (level > (last.level ?? 0)) {
      Object.assign(last, { level, issue: state.latest.verdict.reasons.find((r) => r.level === state.latest.verdict.level)?.text });
    }
    setStatus("live", "Live");
    render();
  };
  source.onerror = () => setStatus("down", "Reconnecting");
}

const TABS = ["overview", "logs", "processes", "config"];
const containerHref = (name, tab) => `#/containers/${encodeURIComponent(name)}${tab && tab !== "overview" ? `/${tab}` : ""}`;
const currentContainer = () => state.latest?.containers?.find((c) => c.name === state.containerName);
const containerPoint = (c, t) => ({ t, cpu: c.cpu, mem: c.memory, readBps: c.readBps, writeBps: c.writeBps, rxBps: c.rxBps, txBps: c.txBps });
const LOG_KEEP = 5000;
const LOG_SHOWN = 2000;
const PROBLEM = /\b(error|err|fatal|panic|exception|traceback|fail(ed|ure)?|critical|denied|refused|timed? ?out)\b/i;

function liveContainerPoint() {
  const c = currentContainer();
  const last = state.points.at(-1);
  if (!c || c.state !== "running" || (last && state.latest.time - last.t < 4500)) return null;
  return containerPoint(c, state.latest.time);
}

function formatDate(value) {
  if (!value || value.startsWith("0001-")) return "-";
  const t = new Date(value);
  return `${t.toLocaleString([], { dateStyle: "medium", timeStyle: "short", hourCycle: "h23" })} · ${formatAgo(t.getTime() / 1000)}`;
}

function route() {
  const match = location.hash.match(/^#\/containers\/([^/]+)(?:\/([a-z]+))?$/);
  const name = match ? decodeURIComponent(match[1]) : null;
  const tab = TABS.includes(match?.[2]) ? match[2] : "overview";
  if (name === state.containerName && (name !== null) === (state.view === "container")) {
    if (name) showTab(tab);
    return;
  }
  if (state.view === "host" && name) state.dashboardScroll = scrollY;
  state.timers.forEach(clearInterval);
  state.timers = [];
  state.view = name ? "container" : "host";
  state.containerName = name;
  state.points = [];
  state.hoverT = null;
  state.pinnedT = null;
  $("dashboard").hidden = Boolean(name);
  $("container-view").hidden = !name;
  if (name) {
    openContainer(name);
    showTab(tab);
    scrollTo(0, 0);
  } else {
    state.issuesKey = "";
    requestAnimationFrame(() => scrollTo(0, state.dashboardScroll));
  }
  loadRange();
  render();
}

function openContainer(name) {
  state.detail = null;
  state.logs = [];
  state.logsAfter = "";
  state.follow = true;
  $("c-name").textContent = name;
  $("c-eyebrow").textContent = "";
  $("c-meta").textContent = "";
  $("c-facts").replaceChildren();
  $("c-notes").replaceChildren();
  $("c-details").replaceChildren();
  $("c-glance").replaceChildren();
  $("c-processes").replaceChildren();
  $("processes-filter").value = "";
  $("config-filter").value = "";
  $("log-filter").value = "";
  state.logView = "all";
  pressOne($("log-streams"), "all");
  ["badge-logs", "badge-processes", "badge-config"].forEach((id) => setBadge($(id), ""));
  $("log-body").replaceChildren(el("p", "log-note", "Loading logs…"));
  $("log-summary").textContent = "";
  $("c-status-title").textContent = "Loading…";
  $("c-status-summary").textContent = "";
  setFollow(true);
  loadDetail();
  loadLogs();
  state.timers.push(setInterval(loadDetail, 5000), setInterval(loadLogs, 2000));
}

async function loadDetail() {
  const name = state.containerName;
  try {
    const res = await fetch(`api/containers/${encodeURIComponent(name)}`, { cache: "no-store" });
    if (name !== state.containerName) return;
    if (res.status === 404) {
      showMissing(name);
      return;
    }
    if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
    state.detail = await res.json();
    $("c-body").hidden = false;
    renderContainerDetail(state.detail);
    renderContainerLive(state.latest);
  } catch (err) {
    $("c-status-summary").textContent = `Details unavailable: ${err.message}`;
  }
}

function showMissing(name) {
  state.detail = null;
  $("c-body").hidden = true;
  $("c-status").dataset.level = "warn";
  $("c-status-icon").replaceChildren(icon("warn"));
  $("c-status-title").textContent = "No such container";
  $("c-status-summary").textContent = `Nothing named ${name} is running or stopped on this server. It may have been removed or renamed.`;
  $("c-notes").replaceChildren();
}

function renderContainerLive(s) {
  const c = currentContainer();
  const d = state.detail;
  if (!c || !d) return;
  const [label, level] = containerState(c);
  const running = c.state === "running";
  const status = $("c-status");
  status.dataset.level = level === "off" ? "ok" : level;
  $("c-status-icon").replaceChildren(icon(level));
  const upFor = c.status.replace(/^Up /, "").replace(/ \(.*\)$/, "");
  let title = "Running";
  let summary = `Up for ${upFor}`;
  if (c.health === "unhealthy") {
    title = "Unhealthy";
    summary = `The health check has failed ${plural(d.healthCheck?.failingStreak ?? 0, "time", "times")} in a row. Up for ${upFor}.`;
  } else if (c.state === "restarting") {
    title = "Restarting";
    summary = `It keeps exiting and has been restarted ${plural(d.restartCount, "time", "times")}.`;
  } else if (c.state === "exited") {
    title = c.exitCode ? `Stopped with exit code ${c.exitCode}` : "Stopped";
    summary = `Exited ${formatAgo(new Date(d.finishedAt).getTime() / 1000)}${d.oomKilled ? ", killed by the kernel for running out of memory" : ""}.`;
  } else if (!running) {
    title = label[0].toUpperCase() + label.slice(1);
    summary = c.status;
  } else if (c.health === "healthy") {
    title = "Running and healthy";
  }
  $("c-status-title").textContent = title;
  $("c-status-summary").textContent = summary;
  $("c-usage").hidden = !running && !state.points.length;

  const facts = [
    [running ? "Up for" : "Stopped", running ? upFor : formatAgo(new Date(d.finishedAt).getTime() / 1000)],
    ["Restarts", count(d.restartCount)],
    ["Processes", running ? count(c.pids) : "-"],
    ["Created", formatAgo(c.created)],
  ];
  $("c-facts").replaceChildren(...dl(facts, "facts").children);

  const notes = [];
  if (d.oomKills) notes.push(["crit", `The kernel has killed a process for running out of memory ${plural(d.oomKills, "time", "times")}`]);
  if (running && c.memLimit && percent(c.memory, c.memLimit) >= 90) notes.push(["warn", `Memory is ${whole(percent(c.memory, c.memLimit))} of its ${formatBytes(c.memLimit)} limit`]);
  if (d.restartCount >= 3) notes.push(["warn", `Restarted ${plural(d.restartCount, "time", "times")}`]);
  if (d.security.privileged) notes.push(["warn", "Runs privileged, with full access to the host"]);
  if (d.security.networkMode === "host") notes.push(["warn", "Shares the host's network stack"]);
  const key = JSON.stringify(notes);
  if ($("c-notes").dataset.key !== key) {
    $("c-notes").dataset.key = key;
    $("c-notes").replaceChildren(
      ...notes.map(([lvl, text]) => {
        const li = el("li", "note-row");
        li.append(icon(lvl), el("span", "text", text));
        return li;
      }),
    );
  }
}

function showTab(tab) {
  state.tab = tab;
  for (const button of $("c-tabs").querySelectorAll("[role=tab]")) {
    const selected = button.dataset.tab === tab;
    button.setAttribute("aria-selected", String(selected));
    button.tabIndex = selected ? 0 : -1;
    $(button.getAttribute("aria-controls")).hidden = !selected;
  }
  if (tab === "logs" && state.follow) $("log-body").scrollTop = $("log-body").scrollHeight;
  if (tab === "overview") renderCharts();
}

function selectTab(tab, focus) {
  history.replaceState(null, "", containerHref(state.containerName, tab));
  showTab(tab);
  if (focus) $(`tab-${tab}`).focus();
  const tabs = $("c-tabs");
  const stuck = tabs.getBoundingClientRect().top <= barHeight() + 1;
  if (stuck || tab === "logs") scrollTo(0, $("c-body").getBoundingClientRect().top + scrollY - barHeight() - tabs.offsetHeight - 16);
}

const barHeight = () => $("bar").getBoundingClientRect().height;

function setBadge(node, text, level) {
  node.textContent = text;
  if (level) node.dataset.level = level;
  else delete node.dataset.level;
}

function pressOne(group, value) {
  for (const button of group.querySelectorAll("button")) button.setAttribute("aria-pressed", String(Object.values(button.dataset)[0] === value));
}

function detailCard(title, parts, wide) {
  const node = el("article", wide ? "card detail wide" : "card detail");
  const header = el("header", "card-head");
  header.append(el("h3", "", title));
  node.append(header, ...parts.filter(Boolean));
  return node;
}

function chips(values, levelOf = () => "") {
  const wrap = el("div", "chips");
  for (const v of values) {
    const chip = el("span", "chip", v);
    const level = levelOf(v);
    if (level) chip.dataset.level = level;
    wrap.append(chip);
  }
  return wrap;
}

function memoryParts(parts, total) {
  if (!parts) return null;
  const slices = [
    ["Application", parts.anon, "s1"],
    ["File cache", parts.file, "s2"],
    ["Kernel", parts.kernel, "s3"],
  ];
  const sum = slices.reduce((t, [, v]) => t + v, 0) || 1;
  const wrap = el("div", "composition");
  const bar = el("div", "stack");
  const legend = el("div", "legend");
  for (const [label, value, cls] of slices.filter(([, v]) => v > 0)) {
    const seg = el("span", `seg ${cls}`);
    seg.style.flexGrow = value / sum;
    seg.title = `${label}: ${formatBytes(value)}`;
    bar.append(seg);
    const part = el("span", "part");
    part.append(el("i", `key block ${cls}`), label, el("b", "", formatBytes(value)));
    legend.append(part);
  }
  wrap.append(el("h4", "", `Memory makeup${total ? ` · ${formatBytes(total)} in use` : ""}`), bar, legend);
  return wrap;
}

const RISKY_CAPS = new Set(["ALL", "SYS_ADMIN", "NET_ADMIN", "SYS_PTRACE", "SYS_MODULE", "DAC_READ_SEARCH", "SYS_RAWIO"]);

function renderContainerDetail(d) {
  $("c-eyebrow").textContent = d.project ? `Compose · ${d.project}${d.service ? ` / ${d.service}` : ""}` : "Container";
  $("c-meta").textContent = [d.image, d.fullId.slice(0, 12), d.platform].filter(Boolean).join(" · ");

  const glance = [];
  const cards = [];
  const published = (d.ports ?? []).filter((p) => p.host);
  glance.push(
    detailCard("Key facts", [
      dl(
        [
          ["Image", d.image],
          ["Restart policy", d.restartPolicy || "no"],
          ["Started", formatDate(d.startedAt)],
          ["Addresses", (d.networks ?? []).map((n) => n.ip).filter(Boolean).join(", ") || (d.security.networkMode === "host" ? "host network" : "-")],
        ],
        "stats",
      ),
      published.length ? itemList(published.map((p) => ({ name: p.container, value: p.host })), "Published ports") : el("p", "note", "No published ports."),
    ]),
  );
  cards.push(
    detailCard("General", [
      dl(
        [
          ["Image", d.image],
          ["Image ID", d.imageId.replace(/^sha256:/, "").slice(0, 12)],
          ["Created", formatDate(d.createdAt)],
          ["Started", formatDate(d.startedAt)],
          ...(d.state !== "running" ? [["Finished", formatDate(d.finishedAt)]] : []),
          ["Restart policy", d.restartPolicy || "no"],
          ["User", d.user || "root (image default)"],
          ["Working directory", d.workingDir || "/"],
        ],
        "stats",
      ),
      dl([["Command", d.command]], "stats single"),
      d.error ? el("p", "hint", d.error) : null,
    ]),
  );

  const limits = d.limits;
  glance.push(
    detailCard("Resources", [
      dl(
        [
          ["CPU limit", limits.cpus ? `${+limits.cpus.toFixed(2)} ${limits.cpus === 1 ? "core" : "cores"}` : "none"],
          ["Memory limit", limits.memory ? formatBytes(limits.memory) : "none", limits.memory ? percent(d.memory, limits.memory) : undefined],
          ["Memory reserved", limits.memoryReservation ? formatBytes(limits.memoryReservation) : "none"],
          ["Processes", limits.pids ? `${count(d.pids)} / ${count(limits.pids)}` : count(d.pids), limits.pids ? percent(d.pids, limits.pids) : undefined],
          ["Out-of-memory kills", count(d.oomKills)],
        ],
        "stats",
      ),
      memoryParts(d.memoryParts, d.memory),
    ]),
  );

  if (d.healthCheck) {
    const h = d.healthCheck;
    const probes = [...h.log].reverse();
    const log = el("div", "probes");
    for (const p of probes) {
      const row = el("div", "probe");
      const top = el("div", "probe-head");
      top.append(tag(p.exitCode === 0 ? "ok" : "crit", p.exitCode === 0 ? "passed" : `failed (${p.exitCode})`), el("span", "sub", clock(new Date(p.start).getTime(), true)));
      row.append(top);
      if (p.output) row.append(el("pre", "", p.output));
      log.append(row);
    }
    const statusLevel = h.status === "healthy" ? "ok" : h.status === "unhealthy" ? "crit" : "off";
    glance.push(
      detailCard("Health check", [
        tag(statusLevel, h.status || "not yet run"),
        dl([["Test", h.test]], "stats single"),
        dl(
          [
            ["Every", `${h.interval}s`],
            ["Timeout", `${h.timeout}s`],
            ["Retries", `${h.retries}`],
            ["Failing streak", `${h.failingStreak}`],
          ],
          "stats",
        ),
        probes.length ? el("h4", "", "Recent results") : null,
        probes.length ? log : null,
      ]),
    );
  }

  const sec = d.security;
  const root = !d.user || d.user === "root" || d.user === "0" || d.user.startsWith("0:");
  cards.push(
    detailCard("Security", [
      itemList([
        { name: "Privileged", value: sec.privileged ? "yes" : "no", level: sec.privileged ? "warn" : "ok" },
        { name: "Runs as", value: root ? "root" : d.user, level: root ? "off" : "ok" },
        { name: "Root filesystem", value: sec.readOnlyRoot ? "read-only" : "writable", level: sec.readOnlyRoot ? "ok" : "off" },
        { name: "Network mode", value: sec.networkMode || "default", level: sec.networkMode === "host" ? "warn" : "ok" },
      ]),
      sec.capAdd?.length ? el("h4", "", "Capabilities added") : null,
      sec.capAdd?.length ? chips(sec.capAdd, (c) => (RISKY_CAPS.has(c.replace(/^CAP_/, "")) ? "warn" : "")) : null,
      sec.capDrop?.length ? el("h4", "", "Capabilities dropped") : null,
      sec.capDrop?.length ? chips(sec.capDrop) : null,
      sec.securityOpt?.length ? el("h4", "", "Security options") : null,
      sec.securityOpt?.length ? chips(sec.securityOpt) : null,
    ]),
  );

  cards.push(
    detailCard("Networking", [
      d.networks?.length ? el("h4", "", "Networks") : el("p", "note", sec.networkMode === "host" ? "Uses the host's network directly." : "Not attached to any network."),
      ...(d.networks ?? []).map((n) => {
        const row = el("div", "mount");
        const head = el("div", "mount-head");
        head.append(el("strong", "", n.name), el("span", "chip", n.ip || "no address"));
        const facts = [n.gateway && `gateway ${n.gateway}`, n.mac && `MAC ${n.mac}`, n.aliases?.length && `also ${n.aliases.join(", ")}`].filter(Boolean);
        row.append(head, el("p", "sub", facts.join(" · ")));
        return row;
      }),
      d.ports?.length
        ? itemList(
            d.ports.map((p) => ({ name: p.container, value: p.host ? `published on ${p.host}` : "not published" })),
            "Ports",
          )
        : null,
    ]),
  );

  cards.push(
    detailCard(
      "Mounts",
      d.mounts?.length
        ? d.mounts.map((m) => {
            const row = el("div", "mount");
            const head = el("div", "mount-head");
            head.append(el("strong", "", m.destination), el("span", "chip", m.readOnly ? "read-only" : "read-write"));
            const origin = m.type === "volume" ? `volume ${/^[0-9a-f]{64}$/.test(m.name) ? `${m.name.slice(0, 12)} (anonymous)` : m.name}` : `${m.type} ${m.source}`;
            const sub = el("p", "sub", origin);
            sub.title = m.source;
            row.append(head, sub);
            return row;
          })
        : [el("p", "note", "No volumes or bind mounts.")],
    ),
  );

  if (d.project) {
    const siblings = (state.latest?.containers ?? []).filter((c) => c.project === d.project).sort((a, b) => a.name.localeCompare(b.name));
    const list = el("div", "list");
    for (const c of siblings) {
      const item = el("div", "item");
      const [label, level] = containerState(c);
      const link = el("a", "", c.service || c.name);
      link.href = containerHref(c.name);
      if (c.name === d.name) link.setAttribute("aria-current", "page");
      const name = el("span", "name");
      name.append(icon(level), link);
      item.append(name, el("span", "value", label));
      list.append(item);
    }
    const configs = (d.labels?.["com.docker.compose.project.config_files"] ?? "").split(",").filter(Boolean);
    const config = dl([["Project", d.project], ["Files", configs.map((f) => f.split("/").pop()).join(", ") || "-"]], "stats");
    if (configs.length) config.lastChild.title = configs.join("\n");
    cards.push(detailCard("Compose project", [config, el("h4", "", "Services"), list]));
  }

  cards.push(detailCard("Environment", [d.env?.length ? chips(d.env) : el("p", "note", "No environment variables."), el("p", "note", "Names only. The monitor never reads the values.")]));

  const labels = Object.entries(d.labels ?? {}).sort(([a], [b]) => a.localeCompare(b));
  cards.push(
    detailCard(
      "Labels",
      [labels.length ? itemList(labels.map(([k, v]) => ({ name: k, value: v, hint: `${k}=${v}` }))) : el("p", "note", "No labels.")],
      true,
    ),
  );
  $("c-glance").replaceChildren(...glance);
  $("c-details").replaceChildren(...cards);
  applyConfigFilter();
  const warnings = [sec.privileged, sec.networkMode === "host", ...(sec.capAdd ?? []).map((c) => RISKY_CAPS.has(c.replace(/^CAP_/, "")))].filter(Boolean).length;
  setBadge($("badge-config"), warnings ? String(warnings) : "", warnings ? "warn" : "");
  renderProcessTable();
}

function renderProcessTable() {
  const d = state.detail;
  if (!d) return;
  const procs = d.processes ?? [];
  const q = $("processes-filter").value.trim().toLowerCase();
  const shown = procs.filter((p) => !q || `${p.pid} ${p.user} ${p.command}`.toLowerCase().includes(q));
  setBadge($("badge-processes"), d.state === "running" ? String(procs.length) : "");
  $("c-processes-summary").textContent = procs.length ? `${q ? `${shown.length} of ` : ""}${plural(procs.length, "process", "processes")}` : "";
  $("c-processes-empty").hidden = shown.length > 0;
  $("c-processes-empty").textContent =
    d.state !== "running" ? "The container is not running." : d.processesError ? `Processes unavailable: ${d.processesError}` : procs.length ? "No processes match." : "No processes.";
  $("c-processes").replaceChildren(
    ...shown.map((p) => {
      const tr = el("tr");
      const cmd = el("td", "cmd", p.command);
      cmd.title = p.command;
      tr.append(el("td", "num", p.pid), el("td", "", p.user), el("td", "num", p.cpu === null ? "-" : `${p.cpu.toFixed(1)}%`), el("td", "num", p.rss ? formatBytes(p.rss) : "-"), el("td", "", p.started || "-"), el("td", "num", p.time || "-"), cmd);
      return tr;
    }),
  );
}

const FILTERABLE = ".item, .chip, .mount, dl > div";

function hideEmptyHeadings(card) {
  const showing = (node) => !node.hidden && (node.matches(FILTERABLE) || !node.querySelector(FILTERABLE) || [...node.querySelectorAll(FILTERABLE)].some((n) => !n.hidden));
  for (const list of card.querySelectorAll(".list, .chips, dl")) list.hidden = ![...list.querySelectorAll(FILTERABLE)].some((n) => !n.hidden);
  let heading = null;
  let any = false;
  for (const child of [...card.children].slice(1)) {
    if (child.tagName === "H4") {
      if (heading) heading.hidden = !any;
      heading = child;
      any = false;
    } else if (showing(child)) {
      any = true;
    }
  }
  if (heading) heading.hidden = !any;
}

function applyConfigFilter() {
  const q = $("config-filter").value.trim().toLowerCase();
  const matches = (node) => `${node.textContent} ${node.title}`.toLowerCase().includes(q);
  let visible = 0;
  let hits = 0;
  for (const card of $("c-details").children) {
    const titleHit = !q || matches(card.querySelector("h3"));
    let any = titleHit;
    for (const item of card.querySelectorAll(FILTERABLE)) {
      const show = titleHit || matches(item);
      item.hidden = !show;
      if (show && q) hits++;
      any ||= show;
    }
    hideEmptyHeadings(card);
    card.hidden = !any;
    if (any) visible++;
  }
  $("config-empty").hidden = visible > 0;
  $("config-summary").textContent = q ? `${plural(hits, "match", "matches")} in ${plural(visible, "section", "sections")}` : "";
}

async function loadLogs() {
  const name = state.containerName;
  const body = $("log-body");
  try {
    const res = await fetch(`api/containers/${encodeURIComponent(name)}/logs${state.logsAfter ? `?since=${state.logsAfter}` : ""}`, { cache: "no-store" });
    if (name !== state.containerName) return;
    if (res.status === 403) {
      body.replaceChildren(el("p", "log-note", "Container logs are turned off. Set MONITOR_CONTAINER_LOGS=true on the Docker proxy to show them here."));
      state.timers.forEach(clearInterval);
      state.timers = [setInterval(loadDetail, 5000)];
      return;
    }
    if (res.status === 404) return;
    if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
    const lines = await res.json();
    const fresh = state.logsAfter ? lines.filter((l) => BigInt(l.id) > BigInt(state.logsAfter)) : lines;
    if (!state.logsAfter && !fresh.length) body.replaceChildren(el("p", "log-note", "No log output yet."));
    if (!fresh.length) return;
    if (!state.logsAfter) body.replaceChildren();
    state.logsAfter = fresh.at(-1).id;
    state.logs.push(...fresh);
    if (state.logs.length > LOG_KEEP) state.logs.splice(0, state.logs.length - LOG_KEEP);
    appendLogLines(fresh);
  } catch (err) {
    if (!state.logs.length) body.replaceChildren(el("p", "log-note", `Logs unavailable: ${err.message}`));
  }
}

function logMatches(line) {
  const filter = $("log-filter").value.trim().toLowerCase();
  if (state.logView === "problems" && !isProblem(line)) return false;
  if (state.logView === "stderr" && line.stream !== "stderr") return false;
  return !filter || line.text.toLowerCase().includes(filter);
}

const isProblem = (line) => PROBLEM.test(line.text);

function logLine(line) {
  const row = el("div", isProblem(line) ? "log-line bad" : "log-line");
  row.dataset.stream = line.stream;
  const time = el("time", "", new Date(line.t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23", fractionalSecondDigits: 3 }));
  time.dateTime = new Date(line.t).toISOString();
  time.title = `${new Date(line.t).toLocaleString()} · ${line.stream}`;
  const text = el("span", "log-text");
  const filter = $("log-filter").value.trim();
  if (filter) {
    const lower = line.text.toLowerCase();
    const needle = filter.toLowerCase();
    let i = 0;
    for (let at = lower.indexOf(needle); at >= 0; at = lower.indexOf(needle, i)) {
      text.append(line.text.slice(i, at), el("mark", "", line.text.slice(at, at + needle.length)));
      i = at + needle.length;
    }
    text.append(line.text.slice(i));
  } else {
    text.textContent = line.text;
  }
  row.append(time, text);
  return row;
}

function appendLogLines(lines) {
  const body = $("log-body");
  body.querySelector(".log-note")?.remove();
  body.append(...lines.filter(logMatches).map(logLine));
  while (body.children.length > LOG_SHOWN) body.firstChild.remove();
  updateLogSummary();
  if (state.follow) body.scrollTop = body.scrollHeight;
}

function rerenderLogs() {
  const body = $("log-body");
  const shown = state.logs.filter(logMatches).slice(-LOG_SHOWN);
  body.replaceChildren(...shown.map(logLine));
  if (!shown.length) body.append(el("p", "log-note", state.logs.length ? "No lines match." : "No log output yet."));
  updateLogSummary();
  if (state.follow) body.scrollTop = body.scrollHeight;
}

function updateLogSummary() {
  const shown = $("log-body").querySelectorAll(".log-line").length;
  const problems = state.logs.filter(isProblem).length;
  $("log-summary").textContent = `${count(shown)} of ${count(state.logs.length)} lines · newest ${state.logs.length ? formatAgo(state.logs.at(-1).t / 1000) : "-"}`;
  setBadge($("badge-logs"), problems ? String(problems) : "", problems ? "crit" : "");
  $("badge-logs").title = problems ? plural(problems, "line mentions a problem", "lines mention a problem") : "";
}

function setFollow(on) {
  state.follow = on;
  const button = $("log-follow");
  button.setAttribute("aria-pressed", String(on));
  button.textContent = on ? "Following" : "Paused · jump to latest";
  if (on) $("log-body").scrollTop = $("log-body").scrollHeight;
}

function bindContainerView() {
  addEventListener("hashchange", route);
  $("log-filter").addEventListener("input", rerenderLogs);
  $("log-streams").addEventListener("click", (e) => {
    const button = e.target.closest("button");
    if (!button) return;
    state.logView = button.dataset.stream;
    pressOne($("log-streams"), state.logView);
    rerenderLogs();
  });
  $("processes-filter").addEventListener("input", renderProcessTable);
  $("config-filter").addEventListener("input", applyConfigFilter);
  $("c-tabs").addEventListener("click", (e) => {
    const button = e.target.closest("[role=tab]");
    if (button) selectTab(button.dataset.tab);
  });
  $("c-tabs").addEventListener("keydown", (e) => {
    const step = { ArrowRight: 1, ArrowLeft: -1 }[e.key];
    if (e.key === "Home" || e.key === "End" || step) {
      e.preventDefault();
      const i = TABS.indexOf(state.tab);
      const next = e.key === "Home" ? 0 : e.key === "End" ? TABS.length - 1 : (i + step + TABS.length) % TABS.length;
      selectTab(TABS[next], true);
    }
  });
  $("log-follow").addEventListener("click", () => setFollow(!state.follow));
  $("log-body").addEventListener("scroll", (e) => {
    const body = e.currentTarget;
    const atBottom = body.scrollHeight - body.scrollTop - body.clientHeight < 24;
    if (atBottom !== state.follow) setFollow(atBottom);
  });
}

const SECTION_REFS = {
  "s-host": (ref) => ["cpu", "memory", "disk", "network", "pressure"].includes(ref),
  "services-section": (ref) => /^(postgres|mysql|mongo|redis|memcached|nginx|rabbitmq):/.test(ref) || ref === "checks" || ref === "systemd",
  "containers-section": (ref) => ref.startsWith("container:"),
  "s-system": (ref) => ["storage", "upkeep", "processes"].includes(ref),
};

function renderSections(s) {
  const reasons = s.verdict.reasons ?? [];
  for (const button of $("sections").querySelectorAll("button")) {
    const id = button.dataset.section;
    button.hidden = $(id).hidden;
    const mine = reasons.filter((r) => SECTION_REFS[id](r.ref));
    const level = mine.some((r) => r.level === "crit") ? "crit" : mine.length ? "warn" : "";
    const total = id === "services-section" ? serviceInstances(s).length + (s.checks?.length ?? 0) : id === "containers-section" ? (s.containers?.length ?? 0) : 0;
    setBadge(button.lastChild, mine.length ? String(mine.length) : total ? String(total) : "", level);
    button.title = mine.length ? plural(mine.length, "problem", "problems") : "";
  }
}

function scrollToSection(id) {
  const smooth = !matchMedia("(prefers-reduced-motion: reduce)").matches;
  const top = $(id).getBoundingClientRect().top + scrollY - barHeight() - $("sections").offsetHeight - 12;
  scrollTo({ top, behavior: smooth ? "smooth" : "auto" });
}

function watchSections() {
  const update = () => {
    if (state.view !== "host") return;
    const line = barHeight() + $("sections").offsetHeight + 32;
    const buttons = [...$("sections").querySelectorAll("button")];
    let current = null;
    for (const button of buttons) {
      const section = $(button.dataset.section);
      if (!section.hidden && section.getBoundingClientRect().top <= line) current = button;
    }
    for (const button of buttons) {
      if (button === current) button.setAttribute("aria-current", "true");
      else button.removeAttribute("aria-current");
    }
  };
  addEventListener("scroll", update, { passive: true });
  new ResizeObserver(() => document.documentElement.style.setProperty("--bar-h", `${barHeight()}px`)).observe($("bar"));
  update();
}

const palette = { items: [], results: [], active: 0, opener: null };

function searchIndex() {
  const s = state.latest;
  if (!s) return [];
  const items = [];
  for (const r of s.verdict.reasons ?? []) items.push({ kind: "Finding", title: r.text, level: r.level, go: () => goTo(r.ref) });
  for (const c of s.containers ?? []) {
    const [label, level] = containerState(c);
    items.push({ kind: "Container", title: c.name, sub: [label, c.image, c.project].filter(Boolean).join(" · "), level, go: () => (location.hash = containerHref(c.name)) });
    items.push({ kind: "Logs", title: `${c.name} logs`, sub: "Open this container's logs", go: () => (location.hash = containerHref(c.name, "logs")), weight: 3 });
  }
  for (const [key, kind, x] of serviceInstances(s)) {
    items.push({ kind: "Service", title: `${kind.title(x)} ${x.name}`, sub: x.up ? "up" : x.error, level: x.up ? "ok" : "crit", go: () => goTo(key) });
  }
  for (const c of s.checks ?? []) items.push({ kind: "Check", title: c.name, sub: c.up ? c.target : `${c.error} · ${c.target}`, level: c.up ? "ok" : "crit", go: () => goTo("checks") });
  if (s.systemd?.up) {
    for (const u of [...s.systemd.failed, ...s.systemd.watched]) {
      items.push({ kind: "Unit", title: u.name, sub: [`${u.active} (${u.sub})`, u.description].filter(Boolean).join(" · "), level: u.active === "active" ? "ok" : "crit", go: () => goTo("systemd") });
    }
  }
  for (const fs of s.filesystems ?? []) items.push({ kind: "Disk", title: fs.mount, sub: `${whole(percent(fs.used, fs.total))} full · ${fs.device}`, go: () => goTo("storage") });
  for (const [title, ref] of [
    ["CPU", "cpu"],
    ["Memory", "memory"],
    ["Disk activity", "disk"],
    ["Network", "network"],
    ["Waiting", "pressure"],
    ["Storage", "storage"],
    ["Busiest processes", "processes"],
    ["Upkeep", "upkeep"],
  ]) {
    items.push({ kind: "Section", title, go: () => goTo(ref), weight: 1 });
  }
  return items;
}

function goTo(ref) {
  if (state.view !== "host" && !ref.startsWith("container:")) {
    location.hash = "#/";
    setTimeout(() => reveal(ref), 80);
    return;
  }
  reveal(ref);
}

function searchResults(query) {
  const q = query.trim().toLowerCase();
  if (!q) return palette.items.filter((i) => i.kind === "Finding" || i.kind === "Container").slice(0, 40);
  const tokens = q.split(/\s+/);
  return palette.items
    .map((item) => {
      const title = item.title.toLowerCase();
      if (!tokens.every((t) => `${title} ${item.sub ?? ""} ${item.kind}`.toLowerCase().includes(t))) return null;
      return { item, score: (title.startsWith(q) ? 0 : title.includes(q) ? 1 : 2) + (item.weight ?? 0) };
    })
    .filter(Boolean)
    .sort((a, b) => a.score - b.score || a.item.title.localeCompare(b.item.title))
    .slice(0, 50)
    .map((r) => r.item);
}

function highlighted(text, query) {
  const node = el("span", "result-title");
  const q = query.trim().toLowerCase();
  const at = q ? text.toLowerCase().indexOf(q) : -1;
  if (at < 0) {
    node.textContent = text;
    return node;
  }
  node.append(text.slice(0, at), el("mark", "", text.slice(at, at + q.length)), text.slice(at + q.length));
  return node;
}

function renderPalette() {
  const list = $("palette-results");
  const q = $("palette-input").value;
  palette.results = searchResults(q);
  palette.active = Math.min(palette.active, Math.max(0, palette.results.length - 1));
  list.replaceChildren(
    ...palette.results.map((item, i) => {
      const li = el("li", "result");
      li.id = `result-${i}`;
      li.setAttribute("role", "option");
      const text = el("span", "result-text");
      text.append(highlighted(item.title, q));
      if (item.sub) text.append(el("span", "result-sub", item.sub));
      li.append(item.level ? icon(item.level) : el("span", "glyph"), text, el("span", "result-kind", item.kind));
      li.addEventListener("click", () => choose(i));
      li.addEventListener("pointermove", () => {
        if (palette.active !== i) {
          palette.active = i;
          markActive(false);
        }
      });
      return li;
    }),
  );
  if (!palette.results.length) list.append(el("li", "result-empty", q.trim() ? `Nothing matches “${q.trim()}”.` : "Nothing to show yet."));
  markActive(true);
}

function markActive(scroll) {
  const items = $("palette-results").querySelectorAll(".result");
  items.forEach((li, i) => li.setAttribute("aria-selected", String(i === palette.active)));
  const active = items[palette.active];
  $("palette-input").setAttribute("aria-activedescendant", active ? active.id : "");
  if (active && scroll) active.scrollIntoView({ block: "nearest" });
}

function openPalette() {
  if (!$("palette").hidden) return;
  palette.opener = document.activeElement;
  palette.items = searchIndex();
  palette.active = 0;
  $("palette-input").value = "";
  $("palette").hidden = false;
  document.body.classList.add("modal-open");
  renderPalette();
  $("palette-input").focus();
}

function closePalette() {
  if ($("palette").hidden) return;
  $("palette").hidden = true;
  document.body.classList.remove("modal-open");
  palette.opener?.focus?.();
}

function choose(i) {
  const item = palette.results[i];
  if (!item) return;
  closePalette();
  item.go();
}

const typing = (target) => target.closest?.("input, textarea, select, [contenteditable]");

function bindNavigation() {
  $("sections").addEventListener("click", (e) => {
    const button = e.target.closest("button");
    if (button) scrollToSection(button.dataset.section);
  });
  $("search-open").addEventListener("click", openPalette);
  $("palette").addEventListener("click", (e) => {
    if (e.target.hasAttribute("data-close")) closePalette();
  });
  $("palette-input").addEventListener("input", () => {
    palette.active = 0;
    renderPalette();
  });
  $("palette-input").addEventListener("keydown", (e) => {
    const n = palette.results.length;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (n) palette.active = (palette.active + (e.key === "ArrowDown" ? 1 : -1) + n) % n;
      markActive(true);
    } else if (e.key === "Enter") {
      e.preventDefault();
      choose(palette.active);
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      closePalette();
    } else if (e.key === "Tab") {
      e.preventDefault();
    }
  });
  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      if ($("palette").hidden) openPalette();
      else closePalette();
    } else if (e.key === "/" && !typing(e.target) && $("palette").hidden) {
      e.preventDefault();
      openPalette();
    }
  });
  $("containers-states").addEventListener("click", (e) => {
    const button = e.target.closest("button");
    if (!button) return;
    state.containerState = button.dataset.state;
    pressOne($("containers-states"), state.containerState);
    if (state.latest) renderContainers(state.latest);
  });
  $("containers-project").addEventListener("change", (e) => {
    state.project = e.target.value;
    if (state.latest) renderContainers(state.latest);
  });
  const servicesView = $("services-section").querySelector(".segmented");
  servicesView.addEventListener("click", (e) => {
    const button = e.target.closest("button");
    if (!button) return;
    state.servicesView = button.dataset.services;
    pressOne(servicesView, state.servicesView);
    if (state.latest) renderServices(state.latest);
  });
  $("services-filter").addEventListener("input", () => state.latest && renderServices(state.latest));
}

const THEMES = ["auto", "light", "dark"];
const THEME_ICONS = {
  auto: "M9 2a7 7 0 1 0 0 14A7 7 0 0 0 9 2zm0 1.5v11a5.5 5.5 0 0 1 0-11z",
  light: "M9 5.5a3.5 3.5 0 1 0 0 7 3.5 3.5 0 0 0 0-7zM9 1v2M9 15v2M1 9h2M15 9h2M3.3 3.3l1.4 1.4M13.3 13.3l1.4 1.4M3.3 14.7l1.4-1.4M13.3 4.7l1.4-1.4",
  dark: "M15 10.6A6.5 6.5 0 0 1 7.4 3a6.5 6.5 0 1 0 7.6 7.6z",
};

function applyTheme(theme) {
  if (theme === "auto") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = theme;
  const button = $("theme");
  button.dataset.theme = theme;
  button.setAttribute("aria-label", `Theme: ${theme === "auto" ? "automatic" : theme}. Change theme`);
  button.title = `Theme: ${theme === "auto" ? "automatic" : theme}`;
  const svg = svgEl("svg", { viewBox: "0 0 18 18", "aria-hidden": "true" });
  const filled = theme !== "light";
  svg.append(svgEl("path", { d: THEME_ICONS[theme], fill: filled ? "currentColor" : "none", stroke: "currentColor", "stroke-width": filled ? 0 : 1.6, "stroke-linecap": "round" }));
  if (theme === "auto") svg.append(svgEl("circle", { cx: 9, cy: 9, r: 6.25, fill: "none", stroke: "currentColor", "stroke-width": 1.5 }));
  button.replaceChildren(svg);
}

function initTheme() {
  let saved = "auto";
  try {
    saved = localStorage.getItem("theme") ?? "auto";
  } catch {}
  applyTheme(THEMES.includes(saved) ? saved : "auto");
  $("theme").addEventListener("click", () => {
    const next = THEMES[(THEMES.indexOf($("theme").dataset.theme) + 1) % THEMES.length];
    applyTheme(next);
    try {
      localStorage.setItem("theme", next);
    } catch {}
  });
}

function watchStale() {
  setInterval(() => {
    if (!state.lastMessage) return;
    const age = Date.now() - state.lastMessage;
    if (age > STALE_AFTER) setStatus("stale", `No updates for ${formatDuration(age / 1000)}`);
  }, 1000);
}

function watchScroll() {
  new IntersectionObserver(([entry]) => $("bar").classList.toggle("scrolled", !entry.isIntersecting), { rootMargin: "-60px 0px 0px 0px" }).observe($("hostname"));
}

initTheme();
document.querySelectorAll(".range button").forEach((b) => b.addEventListener("click", () => selectRange(b.dataset.range)));
document.querySelectorAll(".plot").forEach(bindPlot);
$("containers-filter").addEventListener("input", (e) => {
  state.filter = e.target.value.trim();
  if (state.latest) renderContainers(state.latest);
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && state.pinnedT !== null) {
    state.pinnedT = null;
    renderCursor();
  }
});
bindRibbon();
bindContainerView();
bindNavigation();
watchSections();
watchStale();
watchScroll();
loadHost();
loadTimeline();
setInterval(loadTimeline, MINUTE);
selectRange(state.range);
route();
connect();
