/* 探针 Tanzhen — admin console.

  The console is a separate app on the same stylesheet. It creates nodes, edits
  their subscription metadata, and hands out the one-command installer. The
  node token can be shown again from this panel (GET /api/admin/nodes/{id}/install)
  and is written to a permission-restricted file on the target. Install URLs
  that carry ?token= are stored in reverse-proxy access logs.
 */
(() => {
  "use strict";

  const $ = (s, el = document) => el.querySelector(s);
  const loginView = $("#login-view");
  const dashView = $("#dash-view");
  const editDlg = $("#edit-dlg");
  const installPanel = $("#install-panel");
  const btnTheme = $("#btn-theme");

  /* --------------------------------------------------------------- helpers -- */

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = String(text);
    return e;
  }

  async function api(path, opts = {}) {
    const headers = Object.assign({ "Content-Type": "application/json" }, opts.headers || {});
    const r = await fetch(path, { credentials: "same-origin", ...opts, headers });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) {
      const err = new Error(j.error || r.statusText);
      err.status = r.status;
      throw err;
    }
    return j;
  }

  /* Traffic is configured in bytes but typed by humans as "500GiB". */
  const QUOTA_UNITS = {
    "": 1, b: 1, k: 1024, kb: 1024, kib: 1024,
    m: 1048576, mb: 1048576, mib: 1048576,
    g: 1073741824, gb: 1073741824, gib: 1073741824,
    t: 1099511627776, tb: 1099511627776, tib: 1099511627776,
    p: 1125899906842624, pb: 1125899906842624, pib: 1125899906842624,
  };

  const UNLIMITED_WORDS = /^(无限|不限|不统计|unlimited|inf|none|-)$/i;

  // 0 means "no quota"; null means the input was not understood.
  function parseQuota(s) {
    const t = String(s == null ? "" : s).trim();
    if (!t) return 0;
    if (UNLIMITED_WORDS.test(t)) return 0;
    const m = /^([0-9]*\.?[0-9]+)\s*([a-zA-Z]*)$/.exec(t);
    if (!m) return null;
    const unit = QUOTA_UNITS[m[2].toLowerCase()];
    if (!unit) return null;
    const v = parseFloat(m[1]) * unit;
    if (!isFinite(v) || v < 0) return null;
    return Math.round(v);
  }

  function fmtQuota(n) {
    if (!n) return "";
    const u = [["TiB", 1099511627776], ["GiB", 1073741824], ["MiB", 1048576], ["KiB", 1024]];
    for (const [name, size] of u) {
      if (n >= size) return (n / size).toFixed(n / size < 10 ? 2 : 1).replace(/\.?0+$/, "") + " " + name;
    }
    return n + " B";
  }

  function fmtBytes(n) {
    if (n == null || !isFinite(n) || n < 0) return "—";
    const u = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    let x = Number(n);
    while (x >= 1024 && i < u.length - 1) { x /= 1024; i++; }
    return x.toFixed(i === 0 ? 0 : x < 10 ? 2 : 1) + " " + u[i];
  }

  function fmtPct(n) {
    if (n == null || !isFinite(n) || n < 0) return "—";
    return Number(n).toFixed(1) + "%";
  }

  function fmtClock(s) {
    if (!s) return "";
    const d = new Date(s);
    if (isNaN(d.getTime())) return "";
    const p = (x) => String(x).padStart(2, "0");
    return p(d.getMonth() + 1) + "-" + p(d.getDate()) + " " + p(d.getHours()) + ":" + p(d.getMinutes());
  }

  function shQuote(s) { return "'" + String(s).replace(/'/g, "'\\''") + "'"; }

  /* ------------------------------------------------------------------ auth -- */

  async function checkAuth() {
    try {
      const me = await api("/api/admin/me");
      showDash(me.user);
      return true;
    } catch {
      showLogin();
      return false;
    }
  }

  function showLogin() {
    loginView.classList.remove("hidden");
    dashView.classList.add("hidden");
    $("#login-user").focus();
  }

  function showDash(user) {
    loginView.classList.add("hidden");
    dashView.classList.remove("hidden");
    $("#admin-user-label").textContent = user || "";
    loadAdmin();
    loadAppearance();
  }

  $("#login-form").onsubmit = async (e) => {
    e.preventDefault();
    const errEl = $("#login-err");
    errEl.classList.add("hidden");
    try {
      const res = await api("/api/admin/login", {
        method: "POST",
        body: JSON.stringify({
          username: $("#login-user").value.trim(),
          password: $("#login-pass").value,
        }),
      });
      showDash(res.user);
    } catch (err) {
      errEl.textContent = err.status === 429 ? "尝试过于频繁，请稍后再试" : (err.message || "登录失败");
      errEl.classList.remove("hidden");
    }
  };

  $("#btn-logout").onclick = async () => {
    try { await api("/api/admin/logout", { method: "POST", body: "{}" }); } catch (_) { /* ignore */ }
    $("#login-pass").value = "";
    showLogin();
  };

  $("#btn-refresh").onclick = () => loadAdmin();
  $("#btn-close-install").onclick = () => installPanel.classList.add("hidden");
  $("#btn-cancel-edit").onclick = () => editDlg.close();
  $("#btn-probe-defaults").onclick = () => resetProbeDefaults();
  ["probe-interval", "probe-count", "probe-disable"].forEach((id) => {
    const node = $("#" + id);
    if (!node) return;
    node.addEventListener("change", () => { if (installInfo) renderInstallCmds(installInfo); });
    node.addEventListener("input", () => { if (installInfo) renderInstallCmds(installInfo); });
  });
  // Province checkboxes are created lazily; delegate from the row.
  const provRow = $("#probe-prov-row");
  if (provRow) {
    provRow.addEventListener("change", () => { if (installInfo) renderInstallCmds(installInfo); });
  }

  /* -------------------------------------------------------------- node list -- */

  function trafficLine(n) {
    const t = n.traffic || {};
    const m = n.meta || {};
    if (t.unlimited || !t.period_days) {
      const tot = n.metrics ? (n.metrics.net_total_up || 0) + (n.metrics.net_total_down || 0) : 0;
      return "流量未统计 · 开机累计 " + fmtBytes(tot);
    }
    const parts = ["已用 " + fmtBytes(t.used)];
    if (t.quota) parts.push("/ " + fmtBytes(t.quota) + " (" + fmtPct(t.pct) + ")");
    return "流量 " + parts.join(" ") + " · " + t.period_days + " 天周期";
  }

  async function loadAdmin() {
    try {
      const data = await api("/api/admin/nodes");
      const list = $("#admin-list");
      while (list.firstChild) list.removeChild(list.firstChild);
      const nodes = data.nodes || [];
      if (!nodes.length) {
        list.appendChild(el("p", "muted", "暂无节点。在上方创建后即可复制一键安装命令。"));
        return;
      }
      for (const n of nodes) {
        const row = el("div", "admin-item");
        const who = el("div", "who");
        who.appendChild(el("b", null, n.name || n.id));
        const meta = el("div", "meta");
        meta.appendChild(el("span", "mono", n.id));
        const state = el("span", "state " + (n.online ? "on" : "off"));
        state.appendChild(el("span", "dot"));
        state.appendChild(document.createTextNode(n.online ? "在线" : "离线"));
        meta.appendChild(state);
        // last_seen is Go's zero time ("0001-01-01T00:00:00Z") until the first
        // heartbeat lands - a non-empty string, so test the parsed value.
        if (new Date(n.last_seen || "").getTime() > 0) {
          meta.appendChild(el("span", null, "上报 " + fmtClock(n.last_seen)));
        }
        who.appendChild(meta);
        who.appendChild(el("div", "meta", trafficLine(n)));
        row.appendChild(who);

        const ops = el("div", "ops");
        for (const [act, label, cls] of [["install", "安装命令", "primary"], ["rotate", "轮换 Token", "ghost"], ["edit", "编辑", "ghost"], ["del", "删除", "danger"]]) {
          const b = el("button", "btn btn-sm " + cls, label);
          b.type = "button";
          b.dataset.act = act;
          b.dataset.id = n.id;
          ops.appendChild(b);
        }
        row.appendChild(ops);
        list.appendChild(row);
      }
      list.querySelectorAll("button[data-act]").forEach((btn) => {
        btn.onclick = () => onAdminAct(btn.dataset.act, btn.dataset.id, nodes);
      });
    } catch (e) {
      if (e.status === 401) { showLogin(); return; }
      const list = $("#admin-list");
      while (list.firstChild) list.removeChild(list.firstChild);
      list.appendChild(el("p", "form-err", e.message));
    }
  }

  async function onAdminAct(act, id, nodes) {
    if (act === "del") {
      if (!confirm("确认删除该节点？删除后其 Token 立即失效，已安装的探针将上报失败。")) return;
      try {
        await api("/api/admin/nodes/" + encodeURIComponent(id), { method: "DELETE" });
        loadAdmin();
      } catch (e) { alert(e.message); }
      return;
    }
    if (act === "install") {
      try {
        showInstall(await api("/api/admin/nodes/" + encodeURIComponent(id) + "/install"));
      } catch (e) { alert(e.message); }
      return;
    }
    if (act === "rotate") {
      rotateToken(id);
      return;
    }
    if (act === "edit") {
      const n = (nodes || []).find((x) => x.id === id);
      if (!n) return;
      const m = n.meta || {};
      $("#e-id").value = id;
      $("#e-name").value = n.name || "";
      $("#e-loc").value = m.location || "";
      $("#e-provider").value = m.provider || "";
      $("#e-bw").value = m.bandwidth || "";
      $("#e-renew").value = m.renewal_date || "";
      $("#e-price").value = m.price || "";
      $("#e-quota").value = fmtQuota(m.traffic_quota);
      $("#e-period").value = m.traffic_period ? String(m.traffic_period) : "";
      $("#e-note").value = m.note || "";
      editDlg.showModal();
    }
  }

  /* ------------------------------------------------------------ install cmd -- */

  /* Default representative provinces — keep in sync with agent defaultProvinces. */
  const PROBE_PROVINCES = [
    { code: "bj", label: "北京" },
    { code: "sh", label: "上海" },
    { code: "gd", label: "广东" },
    { code: "js", label: "江苏" },
    { code: "zj", label: "浙江" },
    { code: "sc", label: "四川" },
    { code: "hb", label: "湖北" },
  ];

  let installInfo = null;

  function ensureProbeProvinceChecks() {
    const row = $("#probe-prov-row");
    if (!row || row.dataset.ready === "1") return;
    row.appendChild(el("span", "muted", "探测省份"));
    for (const p of PROBE_PROVINCES) {
      const lab = el("label", "check");
      const cb = document.createElement("input");
      cb.type = "checkbox";
      cb.value = p.code;
      cb.checked = true;
      cb.dataset.probeProv = p.code;
      lab.appendChild(cb);
      lab.appendChild(document.createTextNode(" " + p.label + " (" + p.code + ")"));
      row.appendChild(lab);
    }
    row.dataset.ready = "1";
  }

  function selectedProbeProvinces() {
    ensureProbeProvinceChecks();
    return Array.from(document.querySelectorAll("[data-probe-prov]"))
      .filter((cb) => cb.checked)
      .map((cb) => cb.value);
  }

  function resetProbeDefaults() {
    ensureProbeProvinceChecks();
    $("#probe-interval").value = "30s";
    $("#probe-count").value = "4";
    $("#probe-disable").checked = false;
    document.querySelectorAll("[data-probe-prov]").forEach((cb) => { cb.checked = true; });
    if (installInfo) renderInstallCmds(installInfo);
  }

  function probeQueryParams() {
    const q = {};
    if ($("#probe-disable").checked) {
      q.probe_disable = "1";
      return q;
    }
    const iv = ($("#probe-interval").value || "").trim();
    if (iv && iv !== "30s") q.probe_interval = iv;
    const count = parseInt($("#probe-count").value, 10);
    if (isFinite(count) && count > 0 && count !== 4) q.probe_count = String(count);
    const selected = selectedProbeProvinces();
    const allCodes = PROBE_PROVINCES.map((p) => p.code);
    const isDefault =
      selected.length === allCodes.length &&
      allCodes.every((c) => selected.includes(c));
    if (selected.length && !isDefault) q.probe_provinces = selected.join(",");
    return q;
  }

  function withProbeQuery(url, params) {
    if (!url || !params || !Object.keys(params).length) return url;
    const u = new URL(url, location.origin);
    for (const [k, v] of Object.entries(params)) u.searchParams.set(k, v);
    // Prefer the absolute form from the hub when the input was absolute.
    if (/^https?:\/\//i.test(url)) return u.toString();
    return u.pathname + u.search + u.hash;
  }

  function renderInstallCmds(info) {
    ensureProbeProvinceChecks();
    const params = probeQueryParams();
    const linuxURL = withProbeQuery(info.install_url || "", params);
    const winURL = withProbeQuery(info.win_url || "", params);
    const linuxCmd = linuxURL
      ? "curl -fsSL '" + linuxURL + "' | sh"
      : (info.install_cmd || "");
    const winCmd = winURL
      ? "irm '" + winURL + "' | iex"
      : (info.win_cmd || "");
    $("#linux-cmd").textContent = linuxCmd;
    $("#win-cmd").textContent = winCmd;
    $("#node-token").textContent = info.token || "";

    const hub = String(info.hub_url || "").replace(/\/+$/, "");
    const tok = info.token || "";
    let envExports =
      "export HUB_URL=" + shQuote(hub) + "\n" +
      "export TOKEN=" + shQuote(tok) + "\n";
    if (params.probe_interval) {
      envExports += "export TANZHEN_PROBE_INTERVAL=" + shQuote(params.probe_interval) + "\n";
    }
    if (params.probe_count) {
      envExports += "export TANZHEN_PROBE_COUNT=" + shQuote(params.probe_count) + "\n";
    }
    if (params.probe_provinces) {
      envExports += "export TANZHEN_PROBE_PROVINCES=" + shQuote(params.probe_provinces) + "\n";
    }
    if (params.probe_disable) {
      envExports += "export TANZHEN_PROBE_DISABLE=1\n";
    }
    $("#linux-url").textContent = envExports + "curl -fsSL \"$HUB_URL/install.sh\" | sh";
  }

  function showInstall(info) {
    installInfo = info || null;
    $("#install-title").textContent = "一键安装 · " + ((info && info.name) || "");
    ensureProbeProvinceChecks();
    renderInstallCmds(info || {});
    installPanel.classList.remove("hidden");
    installPanel.scrollIntoView({ behavior: "smooth", block: "nearest" });
  }

  async function rotateToken(id) {
    if (!id) return;
    if (!window.confirm("旧 Token 立即失效，已部署的 agent 需重新安装/更新 token 文件")) return;
    try {
      const res = await api("/api/admin/nodes/" + encodeURIComponent(id) + "/rotate-token", {
        method: "POST",
        body: "{}",
      });
      showInstall(res);
      loadAdmin();
    } catch (e) { alert(e.message); }
  }

  const btnRotate = $("#btn-rotate-token");
  if (btnRotate) {
    btnRotate.onclick = () => {
      const id = installInfo && installInfo.id;
      if (!id) return;
      rotateToken(id);
    };
  }

  function metaFromForm(prefix) {
    const quota = parseQuota($("#" + prefix + "-quota").value);
    if (quota === null) throw new Error("流量配额格式无法识别，示例：500GiB、1TiB、无限");
    const period = parseInt($("#" + prefix + "-period").value, 10);
    return {
      location: $("#" + prefix + "-loc").value.trim(),
      provider: $("#" + prefix + "-provider").value.trim(),
      bandwidth: $("#" + prefix + "-bw").value.trim(),
      renewal_date: $("#" + prefix + "-renew").value.trim(),
      price: $("#" + prefix + "-price").value.trim(),
      traffic_quota: quota,
      traffic_period: isFinite(period) && period > 0 ? period : 0,
      note: $("#" + prefix + "-note") ? $("#" + prefix + "-note").value.trim() : "",
    };
  }

  $("#btn-create").onclick = async () => {
    try {
      const body = { name: $("#n-name").value.trim(), meta: metaFromForm("n") };
      const res = await api("/api/admin/nodes", { method: "POST", body: JSON.stringify(body) });
      showInstall(res);
      ["n-name", "n-loc", "n-provider", "n-bw", "n-renew", "n-price", "n-quota", "n-period"]
        .forEach((id) => { $("#" + id).value = ""; });
      loadAdmin();
    } catch (e) { alert(e.message); }
  };

  $("#btn-save-edit").onclick = async () => {
    const id = $("#e-id").value;
    try {
      await api("/api/admin/nodes/" + encodeURIComponent(id), {
        method: "PATCH",
        body: JSON.stringify({ name: $("#e-name").value.trim(), meta: metaFromForm("e") }),
      });
      editDlg.close();
      loadAdmin();
    } catch (e) { alert(e.message); }
  };

  /* ------------------------------------------------------------------ copy -- */

  async function copyText(text) {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const ta = document.createElement("textarea");
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      ta.remove();
    }
    const toast = $("#copy-toast");
    toast.classList.remove("hidden");
    clearTimeout(copyText._t);
    copyText._t = setTimeout(() => toast.classList.add("hidden"), 1600);
  }

  document.querySelectorAll("[data-copy]").forEach((btn) => {
    btn.onclick = () => {
      const t = $("#" + btn.dataset.copy).textContent.trim();
      if (t) copyText(t);
    };
  });

  /* ----------------------------------------------------------------- theme -- */

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

  btnTheme.addEventListener("click", () => {
    const next = effectiveTheme() === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    try { localStorage.setItem(THEME_KEY, next); } catch (_) { /* ignore */ }
    paintThemeButton();
  });

  try {
    const saved = localStorage.getItem(THEME_KEY);
    if (saved === "light" || saved === "dark") document.documentElement.dataset.theme = saved;
  } catch (_) { /* ignore */ }
  paintThemeButton();

  /* ---------------------------------------------------------- appearance -- */

  const bgState = { url: null, enabled: false, dim: 40, panel: 92, fit: "cover" };

  function applyAppearancePreview(opts) {
    opts = opts || {};
    const dim = opts.dim != null ? opts.dim : Number($("#bg-dim").value);
    const panel = opts.panel != null ? opts.panel : Number($("#bg-panel").value);
    const fit = opts.fit || $("#bg-fit").value || "cover";
    const enabled = opts.enabled != null ? opts.enabled : $("#bg-enabled").checked;
    const url = opts.url !== undefined ? opts.url : bgState.url;
    $("#bg-dim-val").textContent = dim + "%";
    $("#bg-panel-val").textContent = panel + "%";
    const preview = $("#bg-preview");
    const dimLayer = $("#bg-preview-dim");
    const card = $("#bg-preview-card");
    if (!preview || !dimLayer || !card) return;
    if (url && enabled) {
      preview.style.backgroundImage = "url(\"" + String(url).replace(/"/g, "") + "\")";
      preview.style.backgroundSize = fit;
    } else {
      preview.style.backgroundImage = "none";
    }
    dimLayer.style.background = "rgba(0,0,0," + (dim / 100) + ")";
    card.style.background = "color-mix(in oklab, var(--surface) " + panel + "%, transparent)";
  }

  async function loadAppearance() {
    try {
      const a = await api("/api/admin/appearance");
      bgState.url = a.background_url || (a.has_background ? "/media/background" : null);
      bgState.enabled = !!a.enabled;
      bgState.dim = a.dim != null ? a.dim : 40;
      bgState.panel = a.panel_opacity != null ? a.panel_opacity : 92;
      bgState.fit = a.fit || "cover";
      $("#bg-dim").value = bgState.dim;
      $("#bg-panel").value = bgState.panel;
      $("#bg-fit").value = bgState.fit;
      $("#bg-enabled").checked = bgState.enabled;
      const st = $("#appearance-status");
      if (bgState.url && bgState.enabled) {
        st.textContent = "当前：自定义背景已启用";
        st.classList.add("on");
      } else if (a.has_background) {
        st.textContent = "当前：已上传背景（未启用）";
        st.classList.remove("on");
      } else {
        st.textContent = "当前：默认背景";
        st.classList.remove("on");
      }
      const url = bgState.url ? bgState.url.split("?")[0] + "?t=" + Date.now() : null;
      applyAppearancePreview({
        dim: bgState.dim,
        panel: bgState.panel,
        fit: bgState.fit,
        enabled: bgState.enabled,
        url: url,
      });
      if (url) bgState.url = url;
    } catch (e) {
      if (e.status === 401) return;
      console.warn("appearance", e);
    }
  }

  $("#bg-dim").oninput = () => applyAppearancePreview();
  $("#bg-panel").oninput = () => applyAppearancePreview();
  $("#bg-fit").onchange = () => applyAppearancePreview();
  $("#bg-enabled").onchange = () => applyAppearancePreview();

  $("#btn-bg-upload").onclick = async () => {
    const input = $("#bg-file");
    if (!input.files || !input.files[0]) {
      alert("请先选择图片文件");
      return;
    }
    const fd = new FormData();
    fd.append("file", input.files[0]);
    try {
      const r = await fetch("/api/admin/appearance/background", {
        method: "POST",
        credentials: "same-origin",
        body: fd,
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(j.error || r.statusText);
      input.value = "";
      await loadAppearance();
    } catch (e) {
      alert(e.message || "上传失败");
    }
  };

  $("#btn-bg-save").onclick = async () => {
    try {
      await api("/api/admin/appearance", {
        method: "PUT",
        body: JSON.stringify({
          enabled: $("#bg-enabled").checked,
          dim: Number($("#bg-dim").value),
          panel_opacity: Number($("#bg-panel").value),
          fit: $("#bg-fit").value,
          position: "center",
        }),
      });
      await loadAppearance();
    } catch (e) {
      alert(e.message || "保存失败");
    }
  };

  $("#btn-bg-clear").onclick = async () => {
    if (!confirm("清除自定义背景并恢复默认？")) return;
    try {
      const r = await fetch("/api/admin/appearance/background", {
        method: "DELETE",
        credentials: "same-origin",
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(j.error || r.statusText);
      bgState.url = null;
      await loadAppearance();
    } catch (e) {
      alert(e.message || "清除失败");
    }
  };


  checkAuth();
})();
