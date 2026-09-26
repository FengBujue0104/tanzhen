/* 探针 Tanzhen — status page.
 *
 * Layout thesis: an instrument rack. Every node is one ruled block holding four
 * gauges (CPU, 内存, 磁盘, 网络), a traffic meter, the 三网 readout, and the
 * subscription facts. Numbers are tabular so a 2-second refresh never jitters;
 * the only colour is on marks (sparkline strokes, meters, status dots) — text
 * always wears an ink token.
 */
(() => {
  "use strict";

  const $ = (s, el = document) => el.querySelector(s);
  const SVGNS = "http://www.w3.org/2000/svg";
  const NS = "http://www.w3.org/1999/xhtml";

  const rack = $("#rack");
  const tableview = $("#tableview");
  const summaryEl = $("#summary");
  const emptyEl = $("#empty");
  const btnTable = $("#btn-table");
  const btnTheme = $("#btn-theme");
  const sortSel = $("#sort");

  /* ------------------------------------------------------------------ fmt -- */

  const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];

  function fmtBytes(n) {
    if (n == null || !isFinite(n) || n < 0) return "—";
    let i = 0;
    let x = Number(n);
    while (x >= 1024 && i < BYTE_UNITS.length - 1) { x /= 1024; i++; }
    const d = i === 0 ? 0 : x < 10 ? 2 : x < 100 ? 1 : 0;
    return x.toFixed(d) + " " + BYTE_UNITS[i];
  }
  const fmtRate = (n) => fmtBytes(n) + "/s";

  function fmtPct(n, digits = 1) {
    if (n == null || !isFinite(n) || n < 0) return "—";
    return Number(n).toFixed(digits) + "%";
  }

  // -1 means "probe failed", which is different from a fast reply.
  function fmtMs(n) {
    if (n == null || !isFinite(n)) return "—";
    if (n < 0) return "超时";
    return Math.round(n) + " ms";
  }

  function fmtUptime(sec) {
    if (!sec || sec <= 0) return "—";
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    if (d > 0) return d + " 天 " + h + " 小时";
    if (h > 0) return h + " 小时 " + m + " 分";
    return m + " 分钟";
  }

  function fmtClock(unixSec) {
    const d = new Date(unixSec * 1000);
    const p = (n) => String(n).padStart(2, "0");
    return p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds());
  }

  // A node that has never reported serialises its last_seen as Go's zero
  // time, "0001-01-01T00:00:00Z" - a non-empty string, so testing it for
  // truthiness passes and fmtClock() then paints a fabricated clock (that
  // instant renders as 07:36:42 in UTC+8). Parse it and compare to zero.
  function seenAt(s) {
    const t = new Date(s || "").getTime();
    return t > 0 ? Math.floor(t / 1000) : 0;
  }

  // Dates arrive as free text ("2026-12-01", "2026/12/1", ""). Parse the common
  // shapes and fall back to echoing the string, so a note is never lost.
  function parseDate(s) {
    if (!s) return null;
    const m = /^(\d{4})[-/.](\d{1,2})(?:[-/.](\d{1,2}))?/.exec(String(s).trim());
    if (!m) return null;
    const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3] || 1));
    return isNaN(d.getTime()) ? null : d;
  }

  function renewalInfo(s) {
    if (!s || !String(s).trim()) return null;
    const d = parseDate(s);
    if (!d) return { text: String(s), cls: "" };
    const today = new Date();
    today.setHours(0, 0, 0, 0);
    const days = Math.round((d - today) / 86400000);
    const p = (n) => String(n).padStart(2, "0");
    const label = d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate());
    if (days < 0) return { text: label + " · 已过期 " + -days + " 天", cls: "past" };
    if (days === 0) return { text: label + " · 今天到期", cls: "past" };
    if (days <= 30) return { text: label + " · " + days + " 天后", cls: "soon" };
    return { text: label + " · " + days + " 天", cls: "" };
  }

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = String(text);
    return e;
  }

  /* Severity for a utilisation meter: the fill carries state, the number beside
     it carries the value, so colour never means anything on its own. */
  function meterClass(pct) {
    if (pct == null || !isFinite(pct)) return "ok";
    if (pct >= 90) return "crit";
    if (pct >= 75) return "warn";
    return "ok";
  }

  /* 三网 quality: the worse of latency and loss wins. */
  function ispClass(lat, loss) {
    if (lat == null || !isFinite(lat) || lat < 0) return "";
    const l = (loss == null || !isFinite(loss)) ? 0 : loss;
    if (lat > 150 || l >= 10) return "q-bad";
    if (lat > 60 || l > 0) return "q-warn";
    return "q-ok";
  }

  /* ------------------------------------------------------------- sparkline -- */

  const sparks = [];
  const tip = el("div", "tip");
  tip.setAttribute("role", "status");
  document.body.appendChild(tip);

  function svgEl(tag, attrs) {
    const e = document.createElementNS(SVGNS, tag);
    if (attrs) for (const k in attrs) e.setAttribute(k, attrs[k]);
    return e;
  }

  // Round a maximum up to a clean number so the sparkline's implicit top edge
  // is never a coincidence of the data.
  function niceMax(v) {
    if (!(v > 0)) return 1;
    const exp = Math.floor(Math.log10(v));
    const base = Math.pow(10, exp);
    const n = v / base;
    const m = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
    return m * base;
  }

  /**
   * One sparkline plot. All series share a single y-axis (two y-scales on one
   * plot would invent a correlation). Crosshair + tooltip on hover and on
   * keyboard focus; every value is also in the table view.
   */
  class Spark {
    constructor(host) {
      this.host = host;
      this.samples = [];
      this.defs = [];
      this.zeroLine = false;
      this.maxCap = null;
      this.idx = -1;
      this.svg = svgEl("svg", { class: "spark", "aria-hidden": "true" });
      host.appendChild(this.svg);
      sparks.push(this);

      host.addEventListener("pointermove", (e) => this.pick(e));
      host.addEventListener("pointerleave", () => this.clearHover());
    }

    set(samples, defs, opts = {}) {
      this.defs = defs || [];
      this.zeroLine = !!opts.zeroLine;
      this.maxCap = opts.maxCap || null;
      // /api/status ships history samples flat — {t, cpu, mem, up, down} — and
      // each plot only wants its own keys, so derive the per-series values here
      // rather than carrying a nested object over the wire. Doing it in set()
      // keeps the rest of the plot key-agnostic.
      this.samples = (samples || []).map((s) => ({
        t: s.t,
        values: Object.fromEntries(this.defs.map((d) => [d.key, s[d.key]])),
      }));
      this.draw();
    }

    geometry() {
      const w = Math.max(56, this.host.clientWidth || 170);
      const h = 30;
      return { w, h, padX: 5, padY: 5 };
    }

    scales() {
      let rawMax = 0;
      for (const s of this.samples) {
        for (const d of this.defs) {
          const v = s.values[d.key];
          if (v > rawMax) rawMax = v;
        }
      }
      let max = niceMax(rawMax);
      if (this.maxCap) max = Math.min(this.maxCap, Math.max(20, Math.ceil(max / 20) * 20));
      return { max };
    }

    xAt(i, n, g) {
      if (n <= 1) return g.padX;
      return g.padX + (i / (n - 1)) * (g.w - g.padX * 2);
    }

    yAt(v, max, g) {
      const t = max > 0 ? Math.min(1, Math.max(0, v / max)) : 0;
      return g.h - g.padY - t * (g.h - g.padY * 2);
    }

    draw() {
      const { svg } = this;
      while (svg.firstChild) svg.removeChild(svg.firstChild);
      const n = this.samples.length;
      const g = this.geometry();
      svg.setAttribute("viewBox", "0 0 " + g.w + " " + g.h);
      svg.setAttribute("width", g.w);
      svg.setAttribute("height", g.h);
      if (n === 0) return;
      const { max } = this.scales();

      if (this.zeroLine && max > 0) {
        svg.appendChild(svgEl("path", {
          class: "zero",
          d: "M" + g.padX + " " + this.yAt(0, max, g) + " H" + (g.w - g.padX),
        }));
      }

      for (const d of this.defs) {
        if (n === 1) {
          const cx = g.padX + (g.w - g.padX * 2) / 2;
          const dot = svgEl("circle", { class: "end", cx, cy: this.yAt(this.samples[0].values[d.key], max, g) });
          dot.style.fill = d.color;
          svg.appendChild(dot);
          continue;
        }
        let pt = "";
        for (let i = 0; i < n; i++) {
          pt += (i ? "L" : "M") + this.xAt(i, n, g).toFixed(1) + " " +
            this.yAt(this.samples[i].values[d.key], max, g).toFixed(1);
        }
        // Inline style, not a stroke="" presentation attribute: a presentation
        // attribute sits below the stylesheet rule and would lose to it.
        const path = svgEl("path", { class: "line", d: pt });
        path.style.stroke = d.color;
        svg.appendChild(path);
      }

      const last = this.samples[n - 1];
      for (const d of this.defs) {
        const dot = svgEl("circle", { class: "end", cx: this.xAt(n - 1, n, g), cy: this.yAt(last.values[d.key], max, g) });
        dot.style.fill = d.color;
        svg.appendChild(dot);
      }
      if (this.idx >= 0 && this.idx < n) this.paintHover(g, max);
    }

    pick(e) {
      const r = this.svg.getBoundingClientRect();
      const g = this.geometry();
      const x = ((e.clientX - r.left) / r.width) * g.w;
      const n = this.samples.length;
      if (n === 0) return;
      let best = 0;
      let bestD = Infinity;
      for (let i = 0; i < n; i++) {
        const d = Math.abs(this.xAt(i, n, g) - x);
        if (d < bestD) { bestD = d; best = i; }
      }
      this.idx = best;
      this.paintHover(g, this.scales().max);
      this.showTip(e.clientX, e.clientY);
    }

    paintHover(g, max) {
      const stale = this.svg.querySelectorAll(".cross,.hover");
      for (const s of Array.from(stale)) s.remove();
      const n = this.samples.length;
      if (this.idx < 0 || this.idx >= n || n === 0) return;
      const x = this.xAt(this.idx, n, g);
      this.svg.appendChild(svgEl("line", { class: "cross", x1: x, x2: x, y1: 1, y2: g.h - 1 }));
      const s = this.samples[this.idx];
      for (const d of this.defs) {
        const dot = svgEl("circle", { class: "hover", cx: x, cy: this.yAt(s.values[d.key], max, g) });
        dot.style.fill = d.color;
        this.svg.appendChild(dot);
      }
    }

    showTip(px, py) {
      const n = this.samples.length;
      if (this.idx < 0 || this.idx >= n) return;
      const s = this.samples[this.idx];
      while (tip.firstChild) tip.removeChild(tip.firstChild);
      const span = this.host.dataset.span;
      tip.appendChild(el("div", "tip-time", span ? fmtClock(s.t) + " · " + span : fmtClock(s.t)));
      for (const d of this.defs) {
        const row = el("div", "tip-row");
        row.appendChild(el("span", "tip-key")).style.background = d.color;
        row.appendChild(el("span", "tip-name", d.name));
        // textContent, never innerHTML: names and figures come from an API.
        row.appendChild(el("span", "tip-val", d.fmt(s.values[d.key])));
        tip.appendChild(row);
      }
      tip.classList.add("show");
      const w = tip.offsetWidth || 150;
      const h = tip.offsetHeight || 40;
      let left = px + 12;
      if (left + w > window.innerWidth - 8) left = px - w - 12;
      let top = py - h - 10;
      if (top < 8) top = py + 14;
      tip.style.left = Math.max(8, left) + "px";
      tip.style.top = top + "px";
    }

    clearHover() {
      this.idx = -1;
      this.paintHover(this.geometry(), 1);
      tip.classList.remove("show");
    }
  }

  function drawAll() { for (const s of sparks) s.draw(); }

  let resizeTimer = null;
  window.addEventListener("resize", () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(drawAll, 120);
  });

  /* ------------------------------------------------------------ node block -- */

  function gaugeCell(o) {
    const cell = el("div", "gauge" + (o.cls ? " " + o.cls : ""));
    if (o.hue) cell.dataset.hue = o.hue;
    const lab = el("div", "gauge-label");
    lab.appendChild(el("span", "name", o.name));
    if (o.range) lab.appendChild(el("span", "range", o.range));
    cell.appendChild(lab);

    const val = el("div", "gauge-value");
    val.appendChild(document.createTextNode(o.value));
    if (o.unit) val.appendChild(el("span", "unit", o.unit));
    cell.appendChild(val);

    if (o.sub) cell.appendChild(el("div", "gauge-sub", o.sub));
    if (o.spark) cell.appendChild(o.spark);
    if (o.meter != null) {
      const t = el("div", "track " + meterClass(o.meter));
      t.appendChild(el("i"));
      t.lastChild.style.width = Math.max(0, Math.min(100, o.meter)) + "%";
      cell.appendChild(t);
    }
    if (o.extra) cell.appendChild(o.extra);
    return cell;
  }

  function sparkHost(label, span) {
    const d = el("div", "spark");
    d.dataset.label = label;
    d.dataset.span = span || "";
    d.tabIndex = 0;
    d.setAttribute("role", "img");
    d.setAttribute("aria-label", label + "走势图");
    return d;
  }

  function networkLegend(down, up) {
    const wrap = el("div", "net-legend");
    const k1 = el("span", "net-key down");
    k1.appendChild(el("i"));
    k1.appendChild(document.createTextNode("下行 "));
    k1.appendChild(el("b", null, fmtRate(down)));
    const k2 = el("span", "net-key up");
    k2.appendChild(el("i"));
    k2.appendChild(document.createTextNode("上行 "));
    k2.appendChild(el("b", null, fmtRate(up)));
    wrap.append(k1, k2);
    return wrap;
  }

  function disksBlock(disks, primary) {
    const wrap = el("div", "disks");
    const list = disks && disks.length ? disks : (primary ? [primary] : []);
    const shown = list.slice(0, 3);
    for (const d of shown) {
      const row = el("div", "disk");
      row.appendChild(el("span", "disk-mount", d.mount || "/"));
      row.appendChild(el("span", "disk-pct", fmtPct(d.used_pct)));
      const t = el("div", "track " + meterClass(d.used_pct));
      t.appendChild(el("i"));
      t.lastChild.style.width = Math.max(0, Math.min(100, d.used_pct || 0)) + "%";
      row.appendChild(t);
      const parts = [fmtBytes(d.used) + " / " + fmtBytes(d.total)];
      if (d.fs_type) parts.push(d.fs_type);
      row.appendChild(el("span", "disk-size", parts.join(" · ")));
      wrap.appendChild(row);
    }
    if (list.length > shown.length) {
      wrap.appendChild(el("div", "disks-more", "+" + (list.length - shown.length) + " 个挂载点"));
    }
    return wrap;
  }

  function ispRow(m) {
    const row = el("div", "isp");
    const nets = [["电信", m.latency_ct, m.loss_ct], ["联通", m.latency_cu, m.loss_cu], ["移动", m.latency_cm, m.loss_cm]];
    for (const [name, lat, loss] of nets) {
      const c = el("div", "isp-cell " + ispClass(lat, loss));
      c.appendChild(el("div", "isp-net", name));
      c.appendChild(el("div", "isp-lat", fmtMs(lat)));
      const l = (loss == null || !isFinite(loss) || loss < 0) ? "—" : loss.toFixed(1) + "%";
      c.appendChild(el("div", "isp-loss", "丢包 " + l));
      row.appendChild(c);
    }
    return row;
  }

  function tagsBlock(meta) {
    const wrap = el("div", "tags");
    const push = (label, value, cls) => {
      const t = el("span", "tag" + (cls ? " " + cls : ""));
      t.appendChild(document.createTextNode(label + " "));
      t.appendChild(el("b", null, value));
      wrap.appendChild(t);
    };
    if (meta.bandwidth) push("带宽", meta.bandwidth);
    if (meta.provider) push("厂商", meta.provider);
    if (meta.renewal_date) {
      const r = renewalInfo(meta.renewal_date);
      if (r) push("续费", r.text, r.cls);
    }
    if (meta.price) push("价格", meta.price);
    if (meta.note) push("备注", meta.note);
    return wrap;
  }

  function trafficBlock(n) {
    const t = n.traffic || {};
    const m = n.metrics || {};
    const wrap = el("section", "traffic");
    const head = el("div", "traffic-head");
    head.appendChild(el("span", "traffic-title",
      t.unlimited ? "累计流量（自开机）" : "周期流量 · " + (t.period_days || 30) + " 天"));
    const fig = el("span", "traffic-figures");
    if (t.unlimited) {
      // No period configured: fall back to the host's own cumulative counters,
      // which is the only traffic figure that means anything yet.
      fig.appendChild(el("b", null, "↓ " + fmtBytes(m.net_total_down || 0)));
      fig.appendChild(document.createTextNode("  ↑ " + fmtBytes(m.net_total_up || 0)));
      head.appendChild(fig);
      wrap.appendChild(head);
      wrap.appendChild(el("div", "traffic-sub", "未设置流量配额与周期，暂不统计剩余流量。"));
      return wrap;
    }
    fig.appendChild(el("b", null, fmtBytes(t.used)));
    fig.appendChild(document.createTextNode(" / " + (t.quota ? fmtBytes(t.quota) : "未设配额") + " · 已用 " + fmtPct(t.pct)));
    head.appendChild(fig);
    wrap.appendChild(head);

    const bar = el("div", "track " + meterClass(t.pct));
    bar.appendChild(el("i"));
    bar.lastChild.style.width = Math.max(0, Math.min(100, t.pct || 0)) + "%";
    wrap.appendChild(bar);

    const sub = el("div", "traffic-sub");
    const exhausted = t.quota > 0 && t.remaining != null && t.remaining <= t.quota * 0.15;
    sub.appendChild(el("span", exhausted ? (t.pct >= 90 ? "crit" : "warn") : null,
      "剩余 " + (t.quota && t.remaining >= 0 ? fmtBytes(t.remaining) : "—")));
    sub.appendChild(el("span", "↑ " + fmtBytes(t.used_up) + " · ↓ " + fmtBytes(t.used_down)));
    if (t.reset_at) {
      const d = new Date(t.reset_at);
      const p = (x) => String(x).padStart(2, "0");
      sub.appendChild(el("span", null, (d.getMonth() + 1) + "月" + d.getDate() + "日重置"));
    }
    wrap.appendChild(sub);
    return wrap;
  }

  function nodeBlock(n) {
    const m = n.metrics || {};
    const meta = n.meta || {};
    const art = el("article", "node" + (n.online ? "" : " is-offline"));

    /* head */
    const head = el("div", "node-head");
    const id = el("div", "node-id");
    const nameRow = el("div", "node-name");
    nameRow.appendChild(document.createTextNode(n.name || n.id));
    id.appendChild(nameRow);
    const where = el("div", "node-where");
    const bits = [];
    if (meta.location) bits.push(el("b", null, meta.location));
    if (meta.provider) bits.push(document.createTextNode(meta.provider));
    if (m.hostname) bits.push(document.createTextNode(m.hostname));
    if (!bits.length) bits.push(el("span", "dim", n.id));
    for (let i = 0; i < bits.length; i++) {
      if (i) where.appendChild(document.createTextNode(" · "));
      where.appendChild(bits[i]);
    }
    id.appendChild(where);
    head.appendChild(id);

    const right = el("div", "node-head-right");
    const state = el("span", "state " + (n.online ? "on" : "off"));
    state.appendChild(el("span", "dot"));
    state.appendChild(document.createTextNode(n.online ? "在线" : "离线"));
    right.appendChild(state);
    if (n.online && m.uptime) right.appendChild(el("span", "node-uptime", "运行 " + fmtUptime(m.uptime)));
    else {
      const seen = seenAt(n.last_seen);
      if (seen) right.appendChild(el("span", "node-uptime", "最后上报 " + fmtClock(seen)));
    }
    head.appendChild(right);
    art.appendChild(head);

    /* gauges */
    const gauges = el("div", "gauges");
    const span = historySpan(n.history);

    const cpuHost = sparkHost("CPU", span);
    gauges.appendChild(gaugeCell({
      hue: "cpu",
      name: "CPU",
      range: m.cpu_cores ? m.cpu_cores + " 核" : "",
      value: fmtPct(m.cpu_usage),
      sub: loadText(m),
      spark: cpuHost,
      meter: m.cpu_usage,
    }));

    const memHost = sparkHost("内存", span);
    const memSub = m.mem_total ? fmtBytes(m.mem_used) + " / " + fmtBytes(m.mem_total) +
      (m.swap_total ? " · Swap " + fmtBytes(m.swap_used) : "") : "";
    gauges.appendChild(gaugeCell({
      hue: "mem",
      name: "内存",
      range: "",
      value: fmtPct(m.mem_usage),
      sub: memSub,
      spark: memHost,
      meter: m.mem_usage,
    }));

    const primary = primaryDisk(m);
    gauges.appendChild(gaugeCell({
      hue: "disk",
      name: "磁盘",
      range: diskCount(m) + " 个",
      value: fmtPct(primary ? primary.used_pct : m.disk_usage),
      sub: primary ? fmtBytes(primary.used) + " / " + fmtBytes(primary.total) + " · " + (primary.mount || "/") : "",
      spark: null,
      meter: primary ? primary.used_pct : m.disk_usage,
      extra: disksBlock(m.disks, primary),
    }));

    const netHost = sparkHost("网络", span);
    gauges.appendChild(gaugeCell({
      hue: "net",
      name: "网络",
      range: span,
      value: fmtRate(m.net_down),
      unit: "↓ · ↑ " + fmtRate(m.net_up),
      spark: netHost,
      extra: networkLegend(m.net_down, m.net_up),
    }));
    art.appendChild(gauges);

    art.appendChild(trafficBlock(n));
    art.appendChild(ispRow(m));
    const tags = tagsBlock(meta);
    if (tags.childNodes.length) art.appendChild(tags);

    const foot = el("div", "node-foot");
    for (const s of [m.distro, m.kernel ? "kernel " + m.kernel : "", m.arch, m.agent_ver ? "agent v" + m.agent_ver : "", m.processes ? m.processes + " 进程" : ""]) {
      if (s) foot.appendChild(el("span", null, s));
    }
    art.appendChild(foot);

    /* sparklines — one plot per metric, single axis, shared time base */
    const hist = n.history || [];
    const cpuSpark = new Spark(cpuHost);
    const memSpark = new Spark(memHost);
    const netSpark = new Spark(netHost);
    cpuHost._spark = cpuSpark;
    cpuSpark.set(hist, [{ key: "cpu", name: "CPU", fmt: fmtPct }], { maxCap: 100 });
    memSpark.set(hist, [{ key: "mem", name: "内存", fmt: fmtPct }], { maxCap: 100 });
    netSpark.set(hist, [
      { key: "down", name: "下行", color: "var(--s-down)", fmt: fmtRate },
      { key: "up", name: "上行", color: "var(--s-up)", fmt: fmtRate },
    ], { zeroLine: true });

    return art;
  }

  function historySpan(hist) {
    if (!hist || hist.length < 2) return "";
    const secs = hist[hist.length - 1].t - hist[0].t;
    if (secs <= 0) return "";
    if (secs < 90) return "近 " + secs + " 秒";
    return "近 " + Math.round(secs / 60) + " 分钟";
  }

  function loadText(m) {
    const l = m.load;
    if (!l || (!l.l1 && !l.l5 && !l.l15)) return "";
    return "负载 " + [l.l1, l.l5, l.l15].map((v) => (v == null ? "—" : v.toFixed(2))).join(" · ");
  }

  function primaryDisk(m) {
    if (m.disks && m.disks.length) return m.disks[0];
    if (m.disk_total) {
      return { mount: m.disk_mount, used_pct: m.disk_usage, used: m.disk_used, total: m.disk_total };
    }
    return null;
  }

  function diskCount(m) {
    return m.disks && m.disks.length ? m.disks.length : (m.disk_total ? 1 : 0);
  }

  /* ------------------------------------------------------------ summary ----- */

  function renderSummary(nodes) {
    const online = nodes.filter((n) => n.online);
    let down = 0;
    let up = 0;
    let cpuSum = 0;
    let cpuN = 0;
    for (const n of online) {
      down += n.metrics ? n.metrics.net_down || 0 : 0;
      up += n.metrics ? n.metrics.net_up || 0 : 0;
      if (n.metrics && n.metrics.cpu_usage != null) { cpuSum += n.metrics.cpu_usage; cpuN++; }
    }
    let renewing = 0;
    for (const n of nodes) {
      const r = renewalInfo((n.meta || {}).renewal_date);
      if (r && (r.cls === "soon" || r.cls === "past")) renewing++;
    }
    const offline = nodes.length - online.length;

    while (summaryEl.firstChild) summaryEl.removeChild(summaryEl.firstChild);
    const hero = el("div", "fig hero");
    hero.appendChild(el("span", "fig-label", "在线"));
    const hv = el("div", "fig-value");
    hv.appendChild(document.createTextNode(String(online.length)));
    hv.appendChild(el("span", "fig-of", "/ " + nodes.length));
    hero.appendChild(hv);
    summaryEl.appendChild(hero);

    const fig = (label, value, sub) => {
      const f = el("div", "fig");
      f.appendChild(el("span", "fig-label", label));
      f.appendChild(el("div", "fig-value", value));
      if (sub) f.appendChild(el("span", "fig-sub", sub));
      summaryEl.appendChild(f);
    };
    fig("离线", String(offline), offline ? "需检查" : "全部正常");
    fig("总下行", fmtRate(down), "实时合计");
    fig("总上行", fmtRate(up), "实时合计");
    fig("平均 CPU", cpuN ? fmtPct(cpuSum / cpuN) : "—", cpuN + " 个在线节点");
    fig("30 天内到期", String(renewing), renewing ? "含已过期" : "无");
  }

  /* ---------------------------------------------------------- table view ---- */

  function renderTable(nodes) {
    const cols = ["节点", "状态", "位置", "CPU", "内存", "磁盘", "下行", "上行",
      "周期已用", "配额", "剩余", "电信", "联通", "移动", "带宽", "续费", "系统"];
    while (tableview.firstChild) tableview.removeChild(tableview.firstChild);
    const tbl = el("table", "data");
    const thead = el("thead");
    const hr = el("tr");
    for (const c of cols) hr.appendChild(el("th", null, c));
    thead.appendChild(hr);
    tbl.appendChild(thead);
    const tb = el("tbody");
    for (const n of nodes) {
      const m = n.metrics || {};
      const meta = n.meta || {};
      const t = n.traffic || {};
      const disk = primaryDisk(m);
      const renew = renewalInfo(meta.renewal_date);
      const loss = (v) => (v == null || !isFinite(v) || v < 0) ? "—" : v.toFixed(1) + "%";
      const tr = el("tr");
      const cells = [
        n.name || n.id,
        n.online ? "在线" : "离线",
        meta.location || m.hostname || "",
        fmtPct(m.cpu_usage),
        fmtPct(m.mem_usage),
        disk ? fmtPct(disk.used_pct) : "—",
        m.net_down != null ? fmtRate(m.net_down) : "—",
        m.net_up != null ? fmtRate(m.net_up) : "—",
        t.unlimited ? "未统计" : fmtBytes(t.used),
        t.unlimited ? "—" : (t.quota ? fmtBytes(t.quota) : "—"),
        t.unlimited || t.remaining == null || t.remaining < 0 ? "—" : fmtBytes(t.remaining),
        fmtMs(m.latency_ct) + " / " + loss(m.loss_ct),
        fmtMs(m.latency_cu) + " / " + loss(m.loss_cu),
        fmtMs(m.latency_cm) + " / " + loss(m.loss_cm),
        meta.bandwidth || "—",
        renew ? renew.text : "—",
        m.distro || m.os || "—",
      ];
      for (const v of cells) tr.appendChild(el("td", null, v));
      tb.appendChild(tr);
    }
    tbl.appendChild(tb);
    tableview.appendChild(tbl);
  }

  /* -------------------------------------------------------------- render ---- */

  let nodes = [];
  let mode = "rack"; // rack | table

  function sortNodes(list, key) {
    const a = list.slice();
    switch (key) {
      case "cpu": return a.sort((x, y) => val(y, "cpu") - val(x, "cpu"));
      case "mem": return a.sort((x, y) => val(y, "mem") - val(x, "mem"));
      case "traffic": return a.sort((x, y) => val(y, "traffic") - val(x, "traffic"));
      case "name": return a.sort((x, y) => String(x.name).localeCompare(String(y.name), "zh"));
      default: return a;
    }
  }

  function val(n, k) {
    if (!n.metrics) return -1;
    if (k === "cpu") return n.metrics.cpu_usage || 0;
    if (k === "mem") return n.metrics.mem_usage || 0;
    if (k === "traffic") return n.traffic && !n.traffic.unlimited ? n.traffic.pct || 0 : -1;
    return 0;
  }

  function render(data) {
    nodes = data.nodes || [];
    renderSummary(nodes);
    const sorted = sortNodes(nodes, sortSel.value);

    sparks.length = 0;
    while (rack.firstChild) rack.removeChild(rack.firstChild);
    for (const n of sorted) rack.appendChild(nodeBlock(n));

    renderTable(sorted);
    emptyEl.classList.toggle("hidden", nodes.length > 0);
    applyMode();

    $("#updated").textContent = "更新于 " + new Date().toLocaleTimeString();
    $("#clock").textContent = new Date().toLocaleString("zh-CN", { hour12: false });
  }

  async function fetchStatus() {
    const r = await fetch("/api/status", { cache: "no-store" });
    if (!r.ok) throw new Error("status " + r.status);
    return r.json();
  }

  let staleTimer = null;
  async function tick() {
    // Hold the previous render while a slow fetch is in flight: no skeleton, no
    // layout jump. Only dim once it is actually slow.
    clearTimeout(staleTimer);
    staleTimer = setTimeout(() => rack.classList.add("stale"), 400);
    try {
      render(await fetchStatus());
      $("#updated").textContent = "更新于 " + new Date().toLocaleTimeString();
    } catch (e) {
      $("#updated").textContent = "刷新失败：" + e.message;
    } finally {
      clearTimeout(staleTimer);
      rack.classList.remove("stale");
    }
  }

  /* ---------------------------------------------------------------- theme --- */

  const THEME_KEY = "tanzhen-theme";

  function effectiveTheme() {
    const stamp = document.documentElement.dataset.theme;
    if (stamp) return stamp;
    return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
  }

  function paintThemeButton() {
    const dark = effectiveTheme() === "dark";
    btnTheme.textContent = dark ? "亮色" : "暗色";
    btnTheme.title = dark ? "切换到亮色主题" : "切换到暗色主题";
  }

  function applyTheme(stamp) {
    if (stamp) document.documentElement.dataset.theme = stamp;
    else delete document.documentElement.dataset.theme;
    try {
      if (stamp) localStorage.setItem(THEME_KEY, stamp);
      else localStorage.removeItem(THEME_KEY);
    } catch (_) { /* private mode: the choice just doesn't persist */ }
    paintThemeButton();
  }

  btnTheme.addEventListener("click", () => {
    applyTheme(effectiveTheme() === "dark" ? "light" : "dark");
  });

  window.matchMedia("(prefers-color-scheme: light)").addEventListener("change", () => {
    if (!document.documentElement.dataset.theme) paintThemeButton();
  });

  try {
    const saved = localStorage.getItem(THEME_KEY);
    if (saved === "light" || saved === "dark") document.documentElement.dataset.theme = saved;
  } catch (_) { /* ignore */ }
  paintThemeButton();

  /* -------------------------------------------------------------- controls -- */

  btnTable.addEventListener("click", () => {
    mode = mode === "table" ? "rack" : "table";
    btnTable.setAttribute("aria-pressed", String(mode === "table"));
    btnTable.textContent = mode === "table" ? "仪表视图" : "表格视图";
    applyMode();
    if (mode === "rack") drawAll(); // sparklines measured 0 width while hidden
  });

  function applyMode() {
    const showTable = mode === "table" && nodes.length > 0;
    rack.classList.toggle("hidden", showTable);
    tableview.classList.toggle("hidden", !showTable);
    if (showTable) tableview.focus();
  }

  sortSel.addEventListener("change", () => {
    const sorted = sortNodes(nodes, sortSel.value);
    sparks.length = 0;
    while (rack.firstChild) rack.removeChild(rack.firstChild);
    for (const n of sorted) rack.appendChild(nodeBlock(n));
    if (mode === "table") applyMode();
  });

  /* ----------------------------------------------------------- appearance -- */

  function applyAppearance(a) {
    const root = document.documentElement;
    const enabled = a && a.enabled && a.background_url;
    if (!enabled) {
      document.body.classList.remove("has-custom-bg");
      root.style.removeProperty("--bg-image");
      root.style.removeProperty("--bg-dim");
      root.style.removeProperty("--panel-alpha");
      root.style.removeProperty("--bg-fit");
      root.style.removeProperty("--bg-position");
      return;
    }
    const url = String(a.background_url).replace(/"/g, "");
    const dim = Math.max(0, Math.min(100, Number(a.dim != null ? a.dim : 40))) / 100;
    const panel = Math.max(0, Math.min(100, Number(a.panel_opacity != null ? a.panel_opacity : 92))) / 100;
    const fit = a.fit === "contain" ? "contain" : "cover";
    const pos = a.position || "center";
    document.body.classList.add("has-custom-bg");
    root.style.setProperty("--bg-image", 'url("' + url + '")');
    root.style.setProperty("--bg-dim", String(dim));
    root.style.setProperty("--panel-alpha", String(panel));
    root.style.setProperty("--bg-fit", fit);
    root.style.setProperty("--bg-position", pos);
  }

  async function fetchAppearance() {
    try {
      const r = await fetch("/api/appearance");
      if (!r.ok) return;
      applyAppearance(await r.json());
    } catch (e) {
      console.warn(e);
    }
  }


  fetchAppearance();
  tick();
  setInterval(tick, 2000);
})();
