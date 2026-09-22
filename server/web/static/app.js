/* TG WebDAV — vanilla JS UI */
"use strict";

let state = {
  path: "/",
  folders: [],
  files: [],
  defaultFolder: "general",
  version: "",
};

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
}[c]));

/* ---------- api helpers ---------- */
async function api(action, opts = {}) {
  const res = await fetch(`/web/api/${action}`, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}
const apiPOST = (action, body) => api(action, { method: "POST", body: JSON.stringify(body) });

/* ---------- formatting ---------- */
function fmtSize(n) {
  if (n === 0) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), u.length - 1);
  return (n / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1) + " " + u[i];
}
function fmtDate(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return "—";
  return d.toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}
function iconFor(e) {
  if (e.is_dir) return "📁";
  switch (e.kind) {
    case "video": return "🎬";
    case "image": return "🖼️";
    case "audio": return "🎵";
    case "archive": return "📦";
    default: return "📄";
  }
}

/* ---------- navigation ---------- */
function go(path) {
  state.path = path;
  window.location.hash = "#" + encodeURIComponent(path);
  load();
}
function currentPath() {
  const h = decodeURIComponent(window.location.hash.replace(/^#/, ""));
  return h && h.startsWith("/") ? h : "/";
}

async function load() {
  try {
    const data = await api(`list?path=${encodeURIComponent(state.path)}`);
    state.folders = data.folders || [];
    state.files = data.files || [];
    state.defaultFolder = data.default_folder || state.defaultFolder;
    render();
    loadStats();
  } catch (e) {
    toast("Load failed: " + e.message, "err");
  }
}
function refresh() { load(); }

async function loadStats() {
  try {
    const s = await api("stats");
    $("statLeft").textContent = `${s.files} files · ${s.folders} folders · ${fmtSize(s.bytes)} · cache ${fmtSize(s.cache_bytes)}`;
    $("statRight").textContent = `v${s.version}`;
    $("verBadge").textContent = "v" + s.version;
  } catch (_) {}
}

/* ---------- rendering ---------- */
function renderCrumbs() {
  const parts = state.path.split("/").filter(Boolean);
  let html = `<a onclick="go('/')">🏠 root</a>`;
  let acc = "";
  for (const p of parts) {
    acc += "/" + p;
    html += ` <span class="sep">/</span> <a onclick="go('${esc(acc)}')">${esc(p)}</a>`;
  }
  html += ` <span class="sep">/</span> <span class="cur">•</span>`;
  $("crumbs").innerHTML = html;
}

function render() {
  renderCrumbs();
  const q = ($("search").value || "").toLowerCase().trim();
  const folders = state.folders.filter((f) => !q || f.name.toLowerCase().includes(q));
  const files = state.files.filter((f) => !q || f.name.toLowerCase().includes(q));
  const rows = $("rows");
  let html = "";

  for (const f of folders) {
    html += `
      <tr>
        <td class="c-icon"><span class="icon">📁</span></td>
        <td><span class="fname folder" onclick="go('${esc(f.path)}')">${esc(f.name)}</span></td>
        <td class="c-size">—</td>
        <td class="c-date">${fmtDate(f.modified)}</td>
        <td class="c-acts"><span class="rowacts">
          <button class="btn sm ghost" title="Rename" onclick="renamePrompt('${esc(f.path)}','${esc(f.name)}')">✏️</button>
          <button class="btn sm danger" title="Delete" onclick="deletePath('${esc(f.path)}')">🗑</button>
        </span></td>
      </tr>`;
  }
  for (const f of files) {
    const dav = "/dav" + f.path.split("/").map(encodeURIComponent).join("/");
    html += `
      <tr>
        <td class="c-icon"><span class="icon">${iconFor(f)}</span></td>
        <td><span class="fname" onclick="preview('${esc(f.path)}','${esc(f.kind)}')">${esc(f.name)}</span></td>
        <td class="c-size">${fmtSize(f.size)}</td>
        <td class="c-date">${fmtDate(f.modified)}</td>
        <td class="c-acts"><span class="rowacts">
          <button class="btn sm ghost" title="Preview / Play" onclick="preview('${esc(f.path)}','${esc(f.kind)}')">▶</button>
          <button class="btn sm ghost" title="Copy WebDAV link" onclick="copyLink('${esc(f.path)}')">🔗</button>
          <button class="btn sm ghost" title="Move to folder" onclick="movePrompt('${esc(f.path)}')">📤</button>
          <button class="btn sm ghost" title="Rename" onclick="renamePrompt('${esc(f.path)}','${esc(f.name)}')">✏️</button>
          <button class="btn sm danger" title="Delete" onclick="deletePath('${esc(f.path)}')">🗑</button>
        </span></td>
      </tr>`;
  }
  rows.innerHTML = html;
  $("empty").hidden = !!(folders.length + files.length);
}

/* ---------- actions ---------- */
function mkdirPrompt() {
  promptModal("New folder", "Folder name", "", async (name) => {
    await apiPOST("mkdir", { path: joinPath(state.path, name) });
    toast("Folder created", "ok");
    load();
  });
}
function renamePrompt(path, oldName) {
  promptModal("Rename", "New name", oldName, async (name) => {
    await apiPOST("rename", { path, new_name: name });
    toast("Renamed", "ok");
    load();
  });
}
function movePrompt(path) {
  promptModal("Move to folder", "Destination folder (e.g. /movies)", state.defaultFolder, async (dest) => {
    await apiPOST("move", { path, dest: dest.startsWith("/") ? dest : "/" + dest });
    toast("Moved", "ok");
    load();
  });
}
async function deletePath(path) {
  if (!confirm("Delete " + path + " ?" + "\nThis also deletes the Telegram post.")) return;
  await apiPOST("delete", { path });
  toast("Deleted", "ok");
  load();
}
function copyLink(path) {
  const url = location.origin + "/dav" + path.split("/").map(encodeURIComponent).join("/");
  navigator.clipboard.writeText(url).then(
    () => toast("Direct link copied", "ok"),
    () => promptModal("Direct link", "URL", url, () => {})
  );
}

/* ---------- preview ---------- */
function preview(path, kind) {
  const dav = "/dav" + path.split("/").map(encodeURIComponent).join("/");
  const name = path.split("/").pop();
  let body = "";
  if (kind === "video") body = `<video controls autoplay preload="metadata" src="${dav}"></video>`;
  else if (kind === "image") body = `<img src="${dav}" alt="${esc(name)}">`;
  else if (kind === "audio") body = `<audio controls autoplay src="${dav}"></audio>`;
  else if (name.toLowerCase().endsWith(".pdf")) body = `<iframe class="pdf" src="${dav}"></iframe>`;
  else body = `<div class="subtitle">No preview for this type — use the download button below.</div>`;
  openModal(`
    <h3>${iconFor({kind})} ${esc(name)}</h3>
    <div class="subtitle">${esc(path)} · <a href="${dav}" download style="color:var(--accent)">⬇ download / direct link</a></div>
    ${body}
  `);
}

/* ---------- settings ---------- */
async function openSettings() {
  const s = await api("settings");
  promptModal("Settings", "Default folder for bot uploads", (s.default_folder || "/general").replace(/^\//, ""), async (v) => {
    const r = await apiPOST("settings", { default_folder: v });
    state.defaultFolder = r.default_folder;
    toast("Saved: new files go to " + r.default_folder, "ok");
  });
}

/* ---------- upload ---------- */
function uploadFiles(fileList) {
  const files = Array.from(fileList);
  if (!files.length) return;
  openModal(`
    <h3>⬆ Uploading ${files.length} file(s)</h3>
    <div class="subtitle" id="upName">…</div>
    <div class="progress-wrap"><div class="progress-bar" id="upBar"></div></div>
    <div class="modal-actions"><button class="btn" id="upClose" hidden>Close</button></div>
  `);
  let idx = 0;
  const next = () => {
    if (idx >= files.length) {
      $("upName").textContent = "Done ✔";
      $("upClose").hidden = false;
      $("upClose").onclick = () => { closeModal(); load(); };
      load();
      return;
    }
    const f = files[idx++];
    $("upName").textContent = `${f.name} (${fmtSize(f.size)}) — ${idx}/${files.length}`;
    const xhr = new XMLHttpRequest();
    const fd = new FormData();
    fd.append("path", state.path);
    fd.append("file", f);
    xhr.open("POST", "/web/api/upload");
    xhr.upload.onprogress = (ev) => {
      if (ev.lengthComputable) $("upBar").style.width = Math.round((ev.loaded / ev.total) * 100) + "%";
    };
    xhr.onload = () => {
      if (xhr.status !== 200) toast(`Upload failed: ${f.name}`, "err");
      else toast(`Uploaded: ${f.name}`, "ok");
      next();
    };
    xhr.onerror = () => { toast(`Upload failed: ${f.name}`, "err"); next(); };
    xhr.send(fd);
  };
  next();
}

/* ---------- modal helpers ---------- */
function openModal(html) {
  $("modal").innerHTML = html;
  $("overlay").hidden = false;
  $("overlay").onclick = (e) => { if (e.target === $("overlay")) closeModal(); };
}
function closeModal() { $("overlay").hidden = true; $("modal").innerHTML = ""; }
function promptModal(title, label, value, onSubmit) {
  openModal(`
    <h3>${esc(title)}</h3>
    <div class="field"><label>${esc(label)}</label>
      <input type="text" id="pmInput" value="${esc(value || "")}"></div>
    <div class="modal-actions">
      <button class="btn" onclick="closeModal()">Cancel</button>
      <button class="btn primary" id="pmOk">OK</button>
    </div>`);
  const input = $("pmInput");
  input.focus(); input.select();
  const submit = async () => {
    try { await onSubmit(input.value.trim()); closeModal(); }
    catch (e) { toast(e.message, "err"); }
  };
  $("pmOk").onclick = submit;
  input.onkeydown = (e) => { if (e.key === "Enter") submit(); };
}

/* ---------- misc ---------- */
function joinPath(dir, name) {
  return (dir === "/" ? "" : dir) + "/" + name;
}
function toast(msg, cls) {
  const t = document.createElement("div");
  t.className = "toast " + (cls || "");
  t.textContent = msg;
  $("toasts").appendChild(t);
  setTimeout(() => t.remove(), 4000);
}
window.addEventListener("keydown", (e) => { if (e.key === "Escape") closeModal(); });
$("search").addEventListener("input", render);
$("fileInput").addEventListener("change", (e) => { uploadFiles(e.target.files); e.target.value = ""; });

const dz = $("dropzone");
["dragenter", "dragover"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.add("drag"); }));
["dragleave", "drop"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.remove("drag"); }));
dz.addEventListener("drop", (e) => uploadFiles(e.dataTransfer.files));

/* boot */
state.path = currentPath();
window.addEventListener("hashchange", () => { const p = currentPath(); if (p !== state.path) { state.path = p; load(); } });
load();
