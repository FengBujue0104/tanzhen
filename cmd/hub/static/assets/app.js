(() => {
  const $ = (s, el = document) => el.querySelector(s);
  const grid = $("#grid");
  const empty = $("#empty");
  const statsBar = $("#stats-bar");

  const fmtBytes = (n) => {
    if (n == null || isNaN(n)) return "-";
    const u = ["B", "KB", "MB", "GB", "TB"];
    let i = 0; let x = Number(n);
    while (x >= 1024 && i < u.length - 1) { x /= 1024; i++; }
    return x.toFixed(i === 0 ? 0 : 1) + " " + u[i];
  };
  const fmtRate = (n) => fmtBytes(n) + "/s";
  const fmtPct = (n) => (n == null || isNaN(n) ? "-" : Number(n).toFixed(1) + "%");
  const fmtLat = (n) => (n == null || n < 0 || isNaN(n) ? "-" : Math.round(n) + "ms");
  const barClass = (p) => (p >= 90 ? "bar danger" : p >= 75 ? "bar warn" : "bar");

  async function fetchStatus() {
    const r = await fetch("/api/status");
    if (!r.ok) throw new Error("status " + r.status);
    return r.json();
  }

  function render(data) {
    const nodes = data.nodes || [];
    const online = nodes.filter((n) => n.online).length;
    statsBar.innerHTML = `
      <span class="pill">节点 <b>${nodes.length}</b></span>
      <span class="pill">在线 <b style="color:var(--ok)">${online}</b></span>
      <span class="pill">离线 <b style="color:var(--bad)">${nodes.length - online}</b></span>
    `;
    $("#api-hint").textContent = "自动刷新 · " + new Date().toLocaleTimeString();
    $("#clock").textContent = new Date().toLocaleString();

    if (!nodes.length) {
      grid.innerHTML = "";
      empty.classList.remove("hidden");
      return;
    }
    empty.classList.add("hidden");
    grid.innerHTML = nodes.map(cardHTML).join("");
  }

  function cardHTML(n) {
    const m = n.metrics || {};
    const meta = n.meta || {};
    const online = n.online;
    return `
<article class="card">
  <div class="card-h">
    <div>
      <h3>${esc(n.name)}</h3>
      <div class="meta-line">${esc(meta.location || "")}${meta.provider ? " · " + esc(meta.provider) : ""}${m.hostname ? " · " + esc(m.hostname) : ""}</div>
    </div>
    <span class="badge ${online ? "on" : "off"}">${online ? "在线" : "离线"}</span>
  </div>
  <div class="kv">
    <div><div class="k">CPU</div><div class="v">${fmtPct(m.cpu_usage)}</div>
      <div class="${barClass(m.cpu_usage||0)}"><i style="width:${Math.min(100,m.cpu_usage||0)}%"></i></div></div>
    <div><div class="k">内存</div><div class="v">${fmtPct(m.mem_usage)} <span class="muted">(${fmtBytes(m.mem_used)}/${fmtBytes(m.mem_total)})</span></div>
      <div class="${barClass(m.mem_usage||0)}"><i style="width:${Math.min(100,m.mem_usage||0)}%"></i></div></div>
    <div><div class="k">Swap</div><div class="v">${m.swap_total ? fmtBytes(m.swap_used) + " / " + fmtBytes(m.swap_total) : "-"}</div></div>
    <div><div class="k">磁盘</div><div class="v">${fmtPct(m.disk_usage)} <span class="muted">(${fmtBytes(m.disk_used)}/${fmtBytes(m.disk_total)})</span></div>
      <div class="${barClass(m.disk_usage||0)}"><i style="width:${Math.min(100,m.disk_usage||0)}%"></i></div></div>
    <div><div class="k">上行</div><div class="v">${online ? fmtRate(m.net_up) : "-"}</div></div>
    <div><div class="k">下行</div><div class="v">${online ? fmtRate(m.net_down) : "-"}</div></div>
  </div>
  <div class="isp">
    <div><div class="lab">电信</div><div class="val">${fmtLat(m.latency_ct)}</div><div class="lab">丢包 ${fmtPct(m.loss_ct)}</div></div>
    <div><div class="lab">联通</div><div class="val">${fmtLat(m.latency_cu)}</div><div class="lab">丢包 ${fmtPct(m.loss_cu)}</div></div>
    <div><div class="lab">移动</div><div class="val">${fmtLat(m.latency_cm)}</div><div class="lab">丢包 ${fmtPct(m.loss_cm)}</div></div>
  </div>
  <div class="tags">
    ${meta.traffic_remain ? `<span class="tag">剩余流量 <b>${esc(meta.traffic_remain)}</b></span>` : ""}
    ${meta.bandwidth ? `<span class="tag">带宽 <b>${esc(meta.bandwidth)}</b></span>` : ""}
    ${meta.renewal_date ? `<span class="tag">续费 <b>${esc(meta.renewal_date)}</b></span>` : ""}
    ${meta.price ? `<span class="tag">价格 <b>${esc(meta.price)}</b></span>` : ""}
  </div>
</article>`;
  }

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;" }[c]));
  }

  async function loop() {
    try { render(await fetchStatus()); }
    catch (e) { console.warn(e); }
  }

  loop();
  setInterval(loop, 2000);
})();
