(() => {
  const $ = (s, el = document) => el.querySelector(s);
  const loginView = $("#login-view");
  const dashView = $("#dash-view");
  const editDlg = $("#edit-dlg");
  const installPanel = $("#install-panel");

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;" }[c]));
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
  }

  function showDash(user) {
    loginView.classList.add("hidden");
    dashView.classList.remove("hidden");
    $("#admin-user-label").textContent = user || "";
    loadAdmin();
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
      errEl.textContent = err.message || "登录失败";
      errEl.classList.remove("hidden");
    }
  };

  $("#btn-logout").onclick = async () => {
    try { await api("/api/admin/logout", { method: "POST", body: "{}" }); } catch (_) {}
    showLogin();
  };

  $("#btn-refresh").onclick = () => loadAdmin();
  $("#btn-close-install").onclick = () => installPanel.classList.add("hidden");

  async function loadAdmin() {
    try {
      const data = await api("/api/admin/nodes");
      const list = $("#admin-list");
      list.innerHTML = (data.nodes || []).map((n) => `
        <div class="admin-item">
          <div>
            <b>${esc(n.name)}</b>
            <div class="muted" style="font-size:.75rem;display:flex;align-items:center;gap:.45rem;flex-wrap:wrap">
              <span>${esc(n.id)}</span>
              <span class="status ${n.online ? "on" : "off"}"><span class="dot" aria-hidden="true"></span>${n.online ? "在线" : "离线"}</span>
              ${n.meta && n.meta.location ? `<span>· ${esc(n.meta.location)}</span>` : ""}
            </div>
          </div>
          <div class="ops">
            <button type="button" class="btn primary btn-sm" data-act="install" data-id="${esc(n.id)}">安装命令</button>
            <button type="button" class="btn ghost btn-sm" data-act="edit" data-id="${esc(n.id)}">编辑</button>
            <button type="button" class="btn danger btn-sm" data-act="del" data-id="${esc(n.id)}">删除</button>
          </div>
        </div>`).join("") || `<p class="muted">暂无节点，在上方创建后即可复制一键安装命令。</p>`;
      list.querySelectorAll("button[data-act]").forEach((btn) => {
        btn.onclick = () => onAdminAct(btn.dataset.act, btn.dataset.id, data.nodes);
      });
    } catch (e) {
      if (e.status === 401) { showLogin(); return; }
      $("#admin-list").innerHTML = `<p style="color:var(--bad)">${esc(e.message)}</p>`;
    }
  }

  function showInstall(info) {
    $("#install-title").textContent = `一键安装 · ${info.name || ""}`;
    $("#linux-cmd").textContent = info.install_cmd || "";
    $("#linux-url").textContent = info.install_url || "";
    $("#win-cmd").textContent = info.win_cmd || "";
    $("#node-token").textContent = info.token || "";
    installPanel.classList.remove("hidden");
    installPanel.scrollIntoView({ behavior: "smooth", block: "nearest" });
  }

  async function onAdminAct(act, id, nodes) {
    if (act === "del") {
      if (!confirm("确认删除该节点？")) return;
      await api("/api/admin/nodes/" + id, { method: "DELETE" });
      loadAdmin();
      return;
    }
    if (act === "install") {
      const info = await api("/api/admin/nodes/" + id + "/install");
      showInstall(info);
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
      const res = await api("/api/admin/nodes", { method: "POST", body: JSON.stringify(body) });
      showInstall({
        name: res.name,
        token: res.token,
        install_cmd: res.install_cmd,
        install_url: res.install_url,
        win_cmd: res.win_cmd,
      });
      ["n-name","n-loc","n-traffic","n-bw","n-renew","n-price"].forEach((id) => { $(`#${id}`).value = ""; });
      loadAdmin();
    } catch (e) {
      alert(e.message);
    }
  };

  $("#btn-save-edit").onclick = async () => {
    const id = $("#e-id").value;
    try {
      await api("/api/admin/nodes/" + id, {
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
      loadAdmin();
    } catch (e) { alert(e.message); }
  };

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
      const el = $("#" + btn.dataset.copy);
      if (el) copyText(el.textContent.trim());
    };
  });

  checkAuth();
})();
