(() => {
  const $ = (s, el = document) => el.querySelector(s);
  const grid = $("#grid");
  const empty = $("#empty");
  const statsBar = $("#stats-bar");
  const dlg = $("#admin-dlg");
  const editDlg = $("#edit-dlg");
  const TOKEN_KEY = "tanzhen_admin_token";

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

  function token() { return localStorage.getItem(TOKEN_KEY) || ""; }
  function setToken(t) { localStorage.setItem(TOKEN_KEY, t); }

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

  $("#btn-admin").onclick = () => {
    $("#admin-token").value = token();
    dlg.showModal();
    if (token()) loadAdmin();
  };
  $("#btn-save-token").onclick = () => {
    setToken($("#admin-token").value.trim());
    loadAdmin();
  };
  $("#btn-refresh-admin").onclick = () => loadAdmin();

  async function adminFetch(path, opts = {}) {
    const headers = Object.assign({ "Content-Type": "application/json", "X-Admin-Token": token() }, opts.headers || {});
    const r = await fetch(path, { ...opts, headers });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || r.statusText);
    return j;
  }

  async function loadAdmin() {
    try {
      const data = await adminFetch("/api/admin/nodes");
      const list = $("#admin-list");
      list.innerHTML = (data.nodes || []).map((n) => `
        <div class="admin-item">
          <div>
            <b>${esc(n.name)}</b>
            <div class="muted" style="font-size:.75rem">${n.id} · ${n.online ? "在线" : "离线"}</div>
          </div>
          <div class="ops">
            <button type="button" class="btn ghost" data-act="install" data-id="${n.id}">安装命令</button>
            <button type="button" class="btn ghost" data-act="edit" data-id="${n.id}">编辑</button>
            <button type="button" class="btn danger" data-act="del" data-id="${n.id}">删除</button>
          </div>
        </div>`).join("") || `<p class="muted">暂无节点</p>`;
      list.querySelectorAll("button[data-act]").forEach((btn) => {
        btn.onclick = () => onAdminAct(btn.dataset.act, btn.dataset.id, data.nodes);
      });
    } catch (e) {
      $("#admin-list").innerHTML = `<p style="color:var(--bad)">${esc(e.message)}</p>`;
    }
  }

  async function onAdminAct(act, id, nodes) {
    if (act === "del") {
      if (!confirm("确认删除该节点？")) return;
      await adminFetch("/api/admin/nodes/" + id, { method: "DELETE" });
      loadAdmin(); loop();
      return;
    }
    if (act === "install") {
      const info = await adminFetch("/api/admin/nodes/" + id + "/install");
      const out = $("#install-out");
      out.classList.remove("hidden");
      out.textContent = `# Linux 一键扎针\n${info.install_cmd}\n\n# Windows (PowerShell 管理员)\n${info.win_cmd}\n\nHUB=${info.hub_url}\nTOKEN=${info.token}`;
      return;
    }
    if (act === "edit") {
      const n = (nodes || []).find((x) => x.id === id);
      if (!n) return;
      $("#e-id").value = id;
      $("#e-name").value = n.name || "";
      const m = n.meta || {};
      $("#e-loc").value = m.location || "";
      $("#e-traffic").value = m.traffic_remain || "";
      $("#e-bw").value = m.bandwidth || "";
      $("#e-renew").value = m.renewal_date || "";
      $("#e-price").value = m.price || "";
      $("#e-provider").value = m.provider || "";
      $("#e-note").value = m.note || "";
      editDlg.showModal();
    }
  }

  $("#btn-create").onclick = async () => {
    try {
      const body = {
        name: $("#n-name").value.trim(),
        meta: {
          location: $("#n-loc").value.trim(),
          traffic_remain: $("#n-traffic").value.trim(),
          bandwidth: $("#n-bw").value.trim(),
          renewal_date: $("#n-renew").value.trim(),
          price: $("#n-price").value.trim(),
        },
      };
      const res = await adminFetch("/api/admin/nodes", { method: "POST", body: JSON.stringify(body) });
      const out = $("#install-out");
      out.classList.remove("hidden");
      out.textContent = `# 节点 ${res.name} (${res.id})\n# Linux 一键扎针\n${res.install_cmd}\n\n# Windows\n${res.win_cmd}\n\nTOKEN=${res.token}`;
      loadAdmin(); loop();
    } catch (e) {
      alert(e.message);
    }
  };

  $("#btn-save-edit").onclick = async () => {
    const id = $("#e-id").value;
    try {
      await adminFetch("/api/admin/nodes/" + id, {
        method: "PATCH",
        body: JSON.stringify({
          name: $("#e-name").value.trim(),
          meta: {
            location: $("#e-loc").value.trim(),
            traffic_remain: $("#e-traffic").value.trim(),
            bandwidth: $("#e-bw").value.trim(),
            renewal_date: $("#e-renew").value.trim(),
            price: $("#e-price").value.trim(),
            provider: $("#e-provider").value.trim(),
            note: $("#e-note").value.trim(),
          },
        }),
      });
      editDlg.close();
      loadAdmin(); loop();
    } catch (e) { alert(e.message); }
  };

  loop();
  setInterval(loop, 2000);
})();
