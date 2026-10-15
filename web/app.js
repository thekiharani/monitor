const W = 600;
const H = 160;
const PAD = 6;
const SVG_NS = "http://www.w3.org/2000/svg";
const MINUTE = 60 * 1000;
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

const $ = (id) => document.getElementById(id);
let range = "5m";
let points = [];
let latest = null;
let rangeTimer = null;

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
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

function formatDuration(seconds) {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${Math.floor(seconds)}s`;
}

function formatAgo(unixSeconds) {
  if (!unixSeconds) return "never";
  const seconds = Date.now() / 1000 - unixSeconds;
  if (seconds < 90) return "just now";
  return `${formatDuration(seconds)} ago`;
}

function monotonePath(xs, ys) {
  const n = xs.length;
  if (n === 0) return "";
  if (n === 1) return `M${xs[0]},${ys[0]}`;
  const slopes = [];
  for (let i = 0; i < n - 1; i++) slopes.push((ys[i + 1] - ys[i]) / (xs[i + 1] - xs[i]));
  const tangents = [slopes[0]];
  for (let i = 1; i < n - 1; i++) {
    tangents.push(slopes[i - 1] * slopes[i] <= 0 ? 0 : (slopes[i - 1] + slopes[i]) / 2);
  }
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

function svgEl(name, attrs) {
  const node = document.createElementNS(SVG_NS, name);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  return node;
}

function niceMax(value, floor) {
  const v = Math.max(value, floor);
  const exp = 10 ** Math.floor(Math.log10(v));
  for (const step of [1, 2, 2.5, 5, 10]) {
    if (v <= step * exp) return step * exp;
  }
  return 10 * exp;
}

function drawChart(id, series, { floor, cap, label }) {
  const svg = $(`${id}-chart`);
  const peak = Math.max(0, ...series.flatMap((s) => s.values));
  const top = cap ? Math.min(cap, niceMax(peak * 1.15, floor)) : niceMax(peak * 1.15, floor);
  $(`${id}-scale`).textContent = label(top);
  svg.replaceChildren();
  for (let i = 1; i < 4; i++) {
    const y = PAD + ((H - 2 * PAD) * i) / 4;
    svg.append(svgEl("line", { x1: 0, x2: W, y1: y, y2: y, class: "gridline" }));
  }
  const span = RANGES[range].span;
  const end = Math.max(latest?.time ?? 0, points.at(-1)?.t ?? 0);
  const xs = points.map((p) => W - ((end - p.t) / span) * W);
  for (const { values, cls } of series) {
    const n = values.length;
    if (n < 2) continue;
    const ys = values.map((v) => H - PAD - (Math.min(v, top) / top) * (H - 2 * PAD));
    const line = monotonePath(xs, ys);
    svg.append(svgEl("path", { d: `${line} L${xs[n - 1]},${H} L${xs[0]},${H} Z`, class: `area ${cls}` }));
    svg.append(svgEl("path", { d: line, class: `line ${cls}` }));
  }
}

function barLevel(span, value, warn = 70, crit = 90) {
  span.classList.toggle("warn", value >= warn && value < crit);
  span.classList.toggle("crit", value >= crit);
}

function bar(value, thick, warn, crit) {
  const wrap = el("div", thick ? "bar thick" : "bar");
  const fill = el("span");
  fill.style.width = `${Math.min(100, value).toFixed(1)}%`;
  barLevel(fill, value, warn, crit);
  wrap.append(fill);
  return wrap;
}

function rows(target, entries) {
  $(target).replaceChildren(
    ...entries.map(([label, value]) => {
      const row = el("div");
      row.append(el("dt", "", label), el("dd", "", value));
      return row;
    }),
  );
}

function pointOf(s) {
  return {
    t: s.time,
    cpu: s.cpu.total,
    iowait: s.cpu.iowait,
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

function renderCharts() {
  const pick = (key) => points.map((p) => p[key]);
  const pct = (v) => `${Math.round(v)}%`;
  drawChart("cpu", [{ values: pick("cpu"), cls: "s-cpu" }], { floor: 10, cap: 100, label: pct });
  drawChart("mem", [{ values: pick("mem"), cls: "s-mem" }], { floor: 10, cap: 100, label: pct });
  drawChart(
    "disk",
    [
      { values: pick("readBps"), cls: "s-read" },
      { values: pick("writeBps"), cls: "s-write" },
    ],
    { floor: 256 * 1024, label: formatRate },
  );
  drawChart(
    "net",
    [
      { values: pick("rxBps"), cls: "s-rx" },
      { values: pick("txBps"), cls: "s-tx" },
    ],
    { floor: 64 * 1024, label: formatRate },
  );
  if (latest?.pressure) {
    drawChart(
      "psi",
      [
        { values: pick("psiCpu"), cls: "s-cpu" },
        { values: pick("psiMem"), cls: "s-mem" },
        { values: pick("psiIo"), cls: "s-io" },
      ],
      { floor: 5, cap: 100, label: pct },
    );
  }
}

function renderVerdict(s) {
  const v = s.verdict;
  const box = $("verdict");
  box.dataset.level = v.level;
  $("verdict-title").textContent = { ok: "All good", warn: "Needs attention", crit: "Something's wrong" }[v.level];
  const root = (s.filesystems ?? []).find((f) => f.mount === "/") ?? s.filesystems?.[0];
  const running = s.containers?.filter((c) => c.state === "running").length;
  const summary = [
    `CPU ${whole(s.cpu.total)}`,
    `memory ${whole(percent(s.mem.used, s.mem.total))}`,
    root && `disk ${whole(percent(root.used, root.total))} full`,
    s.containers && `${running} of ${s.containers.length} containers running`,
  ];
  $("verdict-summary").textContent = summary.filter(Boolean).join(" · ");
  $("verdict-reasons").replaceChildren(
    ...(v.reasons ?? []).map((r) => {
      const li = el("li", "", r.text);
      li.dataset.level = r.level;
      return li;
    }),
  );
}

function renderCPU(s) {
  $("cpu-value").textContent = Math.round(s.cpu.total);
  $("cpu-user").textContent = whole(s.cpu.user);
  $("cpu-system").textContent = whole(s.cpu.system);
  $("cpu-iowait").textContent = whole(s.cpu.iowait);
  $("cpu-steal").textContent = whole(s.cpu.steal);
  const wrap = $("cores");
  if (wrap.children.length !== s.cpu.cores.length) {
    wrap.replaceChildren(
      ...s.cpu.cores.map((_, i) => {
        const core = el("div", "core");
        core.append(el("span", "", `core ${i}`), bar(0));
        return core;
      }),
    );
  }
  s.cpu.cores.forEach((value, i) => {
    const fill = wrap.children[i].querySelector(".bar span");
    fill.style.width = `${value.toFixed(1)}%`;
    barLevel(fill, value);
  });
}

function renderMemory(s) {
  $("mem-value").textContent = Math.round(percent(s.mem.used, s.mem.total));
  $("mem-used").textContent = `${formatBytes(s.mem.used)} / ${formatBytes(s.mem.total)}`;
  $("mem-cached").textContent = formatBytes(s.mem.cached);
  $("mem-swap").textContent = s.mem.swapTotal ? `${formatBytes(s.mem.swapUsed)} / ${formatBytes(s.mem.swapTotal)}` : "off";
}

function renderIO(s) {
  $("disk-read").textContent = formatRate(s.disk.readBps);
  $("disk-write").textContent = formatRate(s.disk.writeBps);
  const ms = (v, ops) => (ops > 0 ? `${v < 10 ? v.toFixed(1) : Math.round(v)} ms` : "idle");
  $("disk-latency").textContent = `read ${ms(s.disk.readLatency, s.disk.readIops)} · write ${ms(s.disk.writeLatency, s.disk.writeIops)}`;
  $("disk-queue").textContent = s.disk.queue.toFixed(s.disk.queue < 10 ? 1 : 0);
  $("disk-iops").textContent = `${Math.round(s.disk.readIops)} read · ${Math.round(s.disk.writeIops)} write /s`;
  $("disk-busy").textContent = whole(s.disk.busy);

  const rx = s.net.reduce((t, i) => t + i.rxBps, 0);
  const tx = s.net.reduce((t, i) => t + i.txBps, 0);
  $("net-rx").textContent = formatRate(rx);
  $("net-tx").textContent = formatRate(tx);
  rows(
    "ifaces",
    s.net.map((i) => [i.name, `↓ ${formatRate(i.rxBps)} ↑ ${formatRate(i.txBps)}`]),
  );
  $("ifaces").querySelectorAll("dt").forEach((dt) => dt.classList.add("mono"));
}

function renderPressure(s) {
  $("pressure-card").hidden = !s.pressure;
  if (!s.pressure) return;
  $("psi-meters").replaceChildren(
    ...[
      ["CPU", s.pressure.cpu, 50, 80],
      ["Memory", s.pressure.memory, 10, 30],
      ["Disk", s.pressure.io, 30, 60],
    ].map(([label, value, warn, crit]) => {
      const row = el("div", "meter");
      row.append(el("span", "", label), bar(value, false, warn, crit), el("b", "", `${value.toFixed(1)}%`));
      return row;
    }),
  );
}

function renderFilesystems(s) {
  $("filesystems").replaceChildren(
    ...s.filesystems.map((fs) => {
      const used = percent(fs.used, fs.total);
      const item = el("div");
      const head = el("div", "fs-head");
      const title = el("strong", "", fs.mount);
      if (fs.readOnly) title.append(" ", el("span", "pill crit", "read-only"));
      if (fs.errors) title.append(" ", el("span", "pill warn", `${fs.errors} ${fs.errors === 1 ? "error" : "errors"}`));
      head.append(title, el("span", "", `${fs.type} · ${fs.device}`));
      const foot = el(
        "p",
        "fs-foot",
        `${formatBytes(fs.used)} of ${formatBytes(fs.total)} · ${whole(used)} full · ${whole(percent(fs.inodesUsed, fs.inodesTotal))} of inodes`,
      );
      item.append(head, bar(used, true, 85, 95), foot);
      return item;
    }),
  );
}

function containerState(c) {
  if (c.health === "unhealthy") return ["unhealthy", "crit"];
  if (c.state === "restarting") return ["restarting", "crit"];
  if (c.state === "exited") return [c.exitCode ? `exited (${c.exitCode})` : "stopped", c.exitCode ? "warn" : "muted"];
  if (c.health === "starting") return ["starting", "muted"];
  if (c.health === "healthy") return ["healthy", ""];
  return [c.state, c.state === "running" ? "" : "muted"];
}

function renderContainers(s) {
  const card = $("containers-card");
  card.hidden = !s.containers;
  if (!s.containers) return;
  const rank = (c) => ({ crit: 0, warn: 1, "": 2, muted: 3 })[containerState(c)[1]];
  const sorted = [...s.containers].sort((a, b) => rank(a) - rank(b) || b.cpu - a.cpu || a.name.localeCompare(b.name));
  const running = s.containers.filter((c) => c.state === "running").length;
  $("containers-summary").textContent = `${running} running · ${s.containers.length - running} stopped`;
  $("containers").replaceChildren(
    ...sorted.map((c) => {
      const tr = el("tr");
      const name = el("td");
      name.append(el("span", "", c.name), el("small", "", c.image));
      const [label, level] = containerState(c);
      const state = el("td");
      state.append(el("span", `pill ${level}`, label));
      const memory = c.memLimit ? `${formatBytes(c.memory)} / ${formatBytes(c.memLimit)}` : c.memory ? formatBytes(c.memory) : "-";
      const up = c.state === "running" ? c.status.replace(/^Up /, "").replace(/ \(.*\)$/, "") : "-";
      tr.append(
        name,
        state,
        el("td", "num", c.state === "running" ? `${c.cpu.toFixed(1)}%` : "-"),
        el("td", "num", memory),
        el("td", "num", c.state === "running" ? formatRate(c.readBps) : "-"),
        el("td", "num", c.state === "running" ? formatRate(c.writeBps) : "-"),
        el("td", "", up),
      );
      return tr;
    }),
  );
}

function setPill(id, up, error) {
  const pill = $(id);
  pill.textContent = up ? "up" : error || "down";
  pill.className = `pill ${up ? "" : "crit"}`;
}

function renderPostgres(s) {
  const pg = s.postgres;
  $("postgres-card").hidden = !pg;
  if (!pg) return;
  setPill("pg-state", pg.up, pg.error);
  if (!pg.up) {
    rows("pg-rows", []);
    $("pg-dbs").replaceChildren();
    return;
  }
  rows("pg-rows", [
    ["Version", pg.version.split(" ")[0]],
    ["Up for", formatDuration(pg.uptime)],
    ["Connections", `${pg.connections} / ${pg.maxConnections}`],
    ["Running now", `${pg.active}`],
    ["Transactions", `${pg.tps.toFixed(1)}/s`],
    ["Cache hits", `${pg.cacheHit.toFixed(1)}%`],
    ["Longest query", pg.longestQuery ? formatDuration(pg.longestQuery) : "none"],
  ]);
  const dbs = $("pg-dbs");
  dbs.replaceChildren(
    el("h4", "", "Largest databases"),
    ...(pg.databases ?? []).map((d) => {
      const item = el("div", "item");
      item.append(el("span", "mono", d.name), el("span", "mono", formatBytes(d.size)));
      return item;
    }),
  );
}

function renderRedis(s) {
  const r = s.redis;
  $("redis-card").hidden = !r;
  if (!r) return;
  setPill("redis-state", r.up, r.error);
  if (!r.up) {
    rows("redis-rows", []);
    return;
  }
  rows("redis-rows", [
    ["Version", r.version],
    ["Up for", formatDuration(r.uptime)],
    ["Memory", r.maxMemory ? `${formatBytes(r.memory)} / ${formatBytes(r.maxMemory)}` : formatBytes(r.memory)],
    ["Clients", `${r.clients}`],
    ["Commands", `${Math.round(r.opsPerSec)}/s`],
    ["Cache hits", r.hitRate ? `${r.hitRate.toFixed(1)}%` : "-"],
    ["Keys", r.keys.toLocaleString()],
    ["Evicted", r.evicted.toLocaleString()],
  ]);
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
  const drives = h.smart?.disks ?? [];
  $("drives").replaceChildren(
    ...drives.map((d) => {
      const box = el("div", "drive");
      const head = el("div", "drive-head");
      const failing = d.passed === false;
      head.append(el("strong", "", d.model), el("span", `pill ${failing ? "crit" : ""}`, failing ? "failing" : "healthy"));
      box.append(head);
      if (d.lifeLeft !== null && d.lifeLeft !== undefined) {
        const life = el("div", "meter");
        const lifeBar = bar(d.lifeLeft);
        const fill = lifeBar.querySelector("span");
        fill.classList.toggle("warn", d.lifeLeft <= 20 && d.lifeLeft > 10);
        fill.classList.toggle("crit", d.lifeLeft <= 10);
        life.append(el("span", "", "Life left"), lifeBar, el("b", "", `${d.lifeLeft}%`));
        box.append(life);
      }
      const facts = el("dl", "rows");
      const bad = d.reallocated + d.pending + d.uncorrectable;
      [
        ["Temperature", d.tempC ? `${d.tempC}°C` : "-"],
        ["Written", d.writtenBytes ? formatBytes(d.writtenBytes) : "-"],
        ["Powered on", d.powerOnHours ? formatDuration(d.powerOnHours * 3600) : "-"],
        ["Power cuts", `${d.unsafeShutdowns}`],
        ["Bad sectors", `${bad}`],
      ].forEach(([label, value]) => {
        const row = el("div");
        row.append(el("dt", "", label), el("dd", "", value));
        facts.append(row);
      });
      box.append(facts);
      return box;
    }),
  );
  const updates = h.updates
    ? h.updates.pending
      ? `${h.updates.pending} waiting${h.updates.security ? ` · ${h.updates.security} security` : ""}`
      : "up to date"
    : "-";
  rows("upkeep", [
    ["Updates", updates],
    ["Auto-updates ran", formatAgo(h.lastAutoUpdate)],
    ["Reboot needed", h.rebootRequired ? "yes" : "no"],
    ["Drive check", h.smart ? formatAgo(h.smart.checkedAt) : "not set up"],
  ]);
}

function render() {
  const s = latest;
  if (!s) return;
  $("uptime").textContent = formatDuration(s.uptime);
  $("load").textContent = s.load.map((v) => v.toFixed(2)).join(" ");
  $("temp").textContent = s.tempC ? `${Math.round(s.tempC)}°C` : "-";
  renderVerdict(s);
  renderCPU(s);
  renderMemory(s);
  renderIO(s);
  renderPressure(s);
  renderFilesystems(s);
  renderContainers(s);
  renderPostgres(s);
  renderRedis(s);
  renderProcesses(s);
  renderUpkeep(s);
  renderCharts();
}

function setStatus(isLive, text) {
  $("status").classList.toggle("live", isLive);
  $("status-text").textContent = text;
}

async function getJSON(path) {
  const res = await fetch(path, { cache: "no-store" });
  if (!res.ok) throw new Error(res.statusText);
  return res.json();
}

async function loadRange() {
  try {
    const requested = range;
    const fetched = await getJSON(`api/history?range=${requested}`);
    if (requested !== range) return;
    points = fetched;
    renderCharts();
  } catch {
    points = [];
  }
}

function selectRange(next) {
  range = next;
  document.querySelectorAll(".range button").forEach((b) => b.setAttribute("aria-selected", String(b.dataset.range === next)));
  $("range-hint").textContent = RANGES[next].hint;
  clearInterval(rangeTimer);
  rangeTimer = setInterval(loadRange, RANGES[next].live ? 30000 : 60000);
  points = [];
  loadRange();
}

async function loadHost() {
  try {
    const host = await getJSON("api/host");
    $("hostname").textContent = host.hostname || "Server";
    document.title = `${host.hostname} · monitor`;
    $("host-meta").textContent = [host.os, `kernel ${host.kernel}`, host.cpuModel, `${host.cores} cores`].filter(Boolean).join(" · ");
    $("foot").textContent = `Monitoring ${host.hostname} · readings every second · monitor ${host.version}`;
  } catch {
    $("host-meta").textContent = "";
  }
}

function connect() {
  const source = new EventSource("api/stream");
  source.onopen = () => setStatus(true, "Live");
  source.onmessage = (e) => {
    latest = JSON.parse(e.data);
    if (RANGES[range].live) {
      points.push(pointOf(latest));
      const cutoff = latest.time - RANGES[range].span;
      while (points.length && points[0].t < cutoff) points.shift();
    }
    setStatus(true, "Live");
    render();
  };
  source.onerror = () => setStatus(false, "Reconnecting");
}

document.querySelectorAll(".range button").forEach((b) => b.addEventListener("click", () => selectRange(b.dataset.range)));
loadHost();
selectRange(range);
connect();
