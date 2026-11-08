// Pickup phone client.
//
// The URL fragment holds base64url(secret[16] || mac fingerprint[32]). It never
// leaves this browser. We connect over WebRTC, make sure the Mac's DTLS
// fingerprint matches the one from the QR code, and then send the secret inside
// the encrypted data channel.

const app = document.getElementById("app");
const fromEl = document.getElementById("from");
const toEl = document.getElementById("to");
const toastEl = document.getElementById("toast");

const S = {
  key: readKey(),
  pc: null,
  dc: null,
  hello: null,      // {name, items, zip} from the Mac
  loc: null,        // {item, path} of the open folder, null = top level
  lists: new Map(), // "item:path" -> entries
  downloads: new Map(), // key -> download
  transfers: new Map(), // request id -> download
  calls: new Map(),     // request id -> {resolve, reject}
  nextId: 1,
  autoStarted: false,
  thumbs: new Map(),    // "item:path" -> data URL
  thumbQueue: new Map(), // "item:path" -> {item, path, size}
  thumbAsked: new Set(),
  finished: false,      // single file received; we closed the connection on purpose
  retried: false,       // one automatic reconnect per loss
  uploads: [],          // receive mode: files picked on this phone, in order
  sending: new Map(),   // request id -> upload
  pumping: false,
  run: null,            // the current batch, for the total progress on the Mac
};

const banner = document.createElement("div");
banner.className = "banner";
banner.hidden = true;
document.querySelector(".top").after(banner);

// ── Key from the URL fragment ──

function readKey() {
  let frag = location.hash.slice(1);
  try {
    if (frag) {
      sessionStorage.setItem("pickup", frag);
      history.replaceState(null, "", location.pathname); // keep the secret out of the history
    } else {
      frag = sessionStorage.getItem("pickup") || "";
    }
  } catch {}
  let bytes;
  try { bytes = Uint8Array.from(atob(frag.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0)); }
  catch { return null; }
  if (bytes.length !== 48) return null;
  const b64u = (b) => btoa(String.fromCharCode(...b)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return {
    secret: b64u(bytes.subarray(0, 16)),
    fp: [...bytes.subarray(16)].map((b) => b.toString(16).padStart(2, "0")).join(":").toUpperCase(),
  };
}

// ── Connection ──

async function connect() {
  if (!S.key) return showStatus("No code", "Scan the QR code on your Mac to open this page.");
  if (S.hello) showBanner("Reconnecting…");
  else showStatus("Connecting", "Connecting to your Mac…", { spin: true });
  closeConn();

  const pc = new RTCPeerConnection({ iceServers: [] });
  const dc = pc.createDataChannel("pickup", { ordered: true });
  dc.binaryType = "arraybuffer";
  S.pc = pc;
  S.dc = dc;

  try {
    await pc.setLocalDescription(await pc.createOffer());
    await iceComplete(pc, 1500);
  } catch {
    closeConn();
    return showStatus("Not supported", "This browser cannot open a direct connection.");
  }

  let answer;
  try {
    const res = await fetch("/signal", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(pc.localDescription),
    });
    if (res.status === 429) {
      closeConn();
      return problem("Busy", "Too many connection attempts. Try again in a few seconds.");
    }
    if (!res.ok) throw new Error();
    answer = await res.json();
  } catch {
    return ended();
  }

  // The Mac must prove that it is the one from the QR code.
  const m = /a=fingerprint:sha-256 (\S+)/i.exec(answer.sdp || "");
  if (!m || m[1].toUpperCase() !== S.key.fp) {
    closeConn();
    return showStatus("Security check failed", "The device that answered is not your Mac. Make sure you are on a trusted network and scan the code again.");
  }

  dc.onopen = () => send({ t: "auth", secret: S.key.secret });
  dc.onmessage = onMessage;
  dc.onclose = () => { if (S.dc === dc) lost(); };
  pc.onconnectionstatechange = () => {
    if (S.pc === pc && pc.connectionState === "failed") lost();
  };
  const timer = setTimeout(() => { if (S.dc === dc && dc.readyState !== "open") lost(); }, 10000);
  dc.addEventListener("open", () => clearTimeout(timer));

  try { await pc.setRemoteDescription(answer); }
  catch { return lost(); }
}

function iceComplete(pc, timeout) {
  return new Promise((res) => {
    if (pc.iceGatheringState === "complete") return res();
    const t = setTimeout(res, timeout);
    pc.addEventListener("icegatheringstatechange", () => {
      if (pc.iceGatheringState === "complete") { clearTimeout(t); res(); }
    });
  });
}

function closeConn() {
  const { pc, dc } = S;
  S.pc = S.dc = null;
  try { dc?.close(); } catch {}
  try { pc?.close(); } catch {}
  for (const dl of S.transfers.values()) fail(dl, "Connection lost");
  S.transfers.clear();
  for (const c of S.calls.values()) c.reject(new Error("Connection lost"));
  S.calls.clear();
  for (const up of S.sending.values()) upFail(up, "Connection lost");
  S.sending.clear();
}

// The connection dropped, for example because the phone switched apps.
// Reconnect once by ourselves; after that, offer a button.
function lost() {
  closeConn();
  if (S.finished) return;
  if (document.visibilityState !== "visible") return; // reconnect when the page comes back
  if (!S.retried) {
    S.retried = true;
    setTimeout(connect, 300);
    return;
  }
  problem("Connection lost", "The connection to your Mac was interrupted.");
}

// The Mac does not answer: the share was closed or timed out, or the phone
// left the network. We cannot tell which, so keep a way to retry.
function ended() {
  closeConn();
  if (S.hello) return showBanner("Your Mac is not sharing anymore. Files you already downloaded can still be saved.", { retry: true });
  showStatus("Share ended", "Your Mac does not answer. Make sure you are on the same Wi-Fi, or share the file again and scan the new code.", { retry: true });
}

function problem(title, text) {
  if (S.hello) return showBanner(text, { retry: true });
  showStatus(title, text, { retry: true });
}

addEventListener("visibilitychange", () => {
  if (document.visibilityState !== "visible" || S.finished || !S.key) return;
  if (!S.dc || S.dc.readyState === "closed" || S.dc.readyState === "closing") {
    S.retried = true; // this attempt is the automatic one
    connect();
  }
});

function showBanner(text, opts = {}) {
  banner.replaceChildren(h("span", {}, text));
  if (opts.retry) banner.append(h("button", { onclick: connect }, "Reconnect"));
  banner.hidden = false;
}

function hideBanner() {
  banner.hidden = true;
}

function send(msg) {
  S.dc?.send(JSON.stringify(msg));
}

function call(msg) {
  const id = S.nextId++;
  return new Promise((resolve, reject) => {
    S.calls.set(id, { resolve, reject });
    send({ ...msg, id });
  });
}

function onMessage(e) {
  if (typeof e.data !== "string") {
    const id = new DataView(e.data).getUint32(0);
    const dl = S.transfers.get(id);
    if (!dl) return;
    dl.chunks.push(new Uint8Array(e.data, 4));
    dl.received += e.data.byteLength - 4;
    dl.notify();
    return;
  }
  const m = JSON.parse(e.data);
  switch (m.t) {
    case "hello":
      S.hello = m;
      S.retried = false;
      hideBanner();
      if (m.mode === "receive") {
        fromEl.textContent = "This phone";
        toEl.textContent = m.name || "Your Mac";
      } else {
        fromEl.textContent = m.name || "Your Mac";
      }
      // Files that the lost connection interrupted: send them again.
      for (const up of S.uploads) {
        if (up.state === "error" && up.error === "Connection lost") { joinRun(up); up.state = "queued"; }
      }
      render();
      pump();
      for (const k of S.thumbQueue.keys()) S.thumbAsked.delete(k); // ask again on the new connection
      flushThumbs();
      break;
    case "thumb": {
      const key = `${m.item}:${m.path}`;
      S.thumbs.set(key, m.url);
      S.thumbQueue.delete(key);
      for (const el of document.querySelectorAll("[data-thumb]")) {
        if (el.dataset.thumb === key) setThumb(el, m.url);
      }
      break;
    }
    case "list": {
      const c = S.calls.get(m.id);
      S.calls.delete(m.id);
      c?.resolve(m.entries);
      break;
    }
    case "start": {
      const dl = S.transfers.get(m.id);
      if (!dl) return;
      Object.assign(dl, { name: m.name, size: m.size, approx: !!m.approx, mime: m.mime });
      dl.notify();
      break;
    }
    case "end": {
      const dl = S.transfers.get(m.id);
      S.transfers.delete(m.id);
      if (dl) finish(dl);
      break;
    }
    case "folder":
      // The Mac user picked another folder for new files.
      if (S.hello?.mode === "receive") { S.hello.folder = m.folder; renderInbox(); }
      break;
    case "saved": {
      const up = S.sending.get(m.id);
      S.sending.delete(m.id);
      if (up) { up.state = "done"; up.savedAs = m.name; leaveRun(up, true); up.notify(); up.settle?.(); }
      break;
    }
    case "error": {
      const up = S.sending.get(m.id);
      if (up) { S.sending.delete(m.id); upFail(up, m.message); }
      const c = S.calls.get(m.id);
      if (c) { S.calls.delete(m.id); c.reject(new Error(m.message)); }
      const dl = S.transfers.get(m.id);
      if (dl) { S.transfers.delete(m.id); fail(dl, m.message); }
      break;
    }
  }
}

// ── Downloads ──

function download(key, req, name) {
  let dl = S.downloads.get(key);
  if (dl?.state === "done") return save(dl);
  if (dl?.state === "loading") return;
  if (!S.dc || S.dc.readyState !== "open") return toast("Not connected");
  dl = dl || makeDownload(key, name);
  Object.assign(dl, { state: "loading", chunks: [], received: 0, size: 0, error: null });
  const id = S.nextId++;
  S.transfers.set(id, dl);
  send({ ...req, id });
  dl.notify();
}

function makeDownload(key, name) {
  const dl = {
    key, name, state: "idle", chunks: [], received: 0, size: 0, approx: false,
    mime: "", url: null, error: null, listeners: new Set(),
    notify() { for (const f of dl.listeners) f(dl); },
  };
  S.downloads.set(key, dl);
  return dl;
}

function finish(dl) {
  const blob = new Blob(dl.chunks, { type: dl.mime || "application/octet-stream" });
  dl.chunks = [];
  dl.size = dl.received;
  dl.url = URL.createObjectURL(blob);
  dl.state = "done";
  dl.notify();
  save(dl);
  // A single shared file is now on the phone. Disconnect, so the Mac can stop
  // sharing. "Save again" still works from memory.
  if (S.hello?.single) {
    S.finished = true;
    showBanner("Picked up. Your Mac has stopped sharing.");
    // Let a preview that is still on its way arrive first.
    const t0 = Date.now();
    const wait = () => (S.thumbQueue.size && Date.now() - t0 < 2000 ? setTimeout(wait, 50) : closeConn());
    wait();
  }
}

function fail(dl, msg) {
  dl.chunks = [];
  dl.state = "error";
  dl.error = msg;
  dl.notify();
  toast(`${display(dl.name)}: ${msg}`);
}

function save(dl) {
  const a = document.createElement("a");
  a.href = dl.url;
  a.download = display(dl.name);
  document.body.append(a);
  a.click();
  a.remove();
}

// ── Thumbnails ──
// Rendered by Quick Look on the Mac, only for rows that scroll into view.

const THUMBABLE = /\.(jpe?g|png|gif|webp|heic|heif|avif|bmp|tiff?|mp4|mov|m4v|pdf)$/i;
const thumbable = (name) => THUMBABLE.test(name);

const thumbObserver = new IntersectionObserver((entries) => {
  for (const en of entries) {
    if (!en.isIntersecting) continue;
    thumbObserver.unobserve(en.target);
    const { thumb, size } = en.target.dataset;
    const [item, ...rest] = thumb.split(":");
    S.thumbQueue.set(thumb, { item: +item, path: rest.join(":"), size: +size });
  }
  flushThumbs();
}, { rootMargin: "200px" });

// eager: ask right away instead of waiting until the element is visible.
function wantThumb(el, item, path, size, eager = false) {
  const key = `${item}:${path}`;
  el.dataset.thumb = key;
  el.dataset.size = size;
  const url = S.thumbs.get(key);
  if (url) return setThumb(el, url);
  if (!eager) return thumbObserver.observe(el);
  S.thumbQueue.set(key, { item, path, size });
  flushThumbs();
}

function setThumb(el, url) {
  const img = h("img", { src: url, alt: "", decoding: "async" });
  el.replaceChildren(img);
  el.classList.add("thumb");
}

let thumbTimer;
function flushThumbs() {
  clearTimeout(thumbTimer);
  thumbTimer = setTimeout(() => {
    if (!S.dc || S.dc.readyState !== "open" || !S.hello) return;
    const groups = new Map(); // "item:size" -> paths
    for (const [key, q] of S.thumbQueue) {
      if (S.thumbAsked.has(key)) continue;
      S.thumbAsked.add(key);
      const g = `${q.item}:${q.size}`;
      if (!groups.has(g)) groups.set(g, []);
      groups.get(g).push(q.path);
    }
    for (const [g, paths] of groups) {
      const [item, size] = g.split(":").map(Number);
      for (let i = 0; i < paths.length; i += 12) {
        send({ t: "thumbs", item, size, paths: paths.slice(i, i + 12) });
      }
    }
  }, 80);
}

// ── Navigation ──

function rootLoc() {
  const items = S.hello.items;
  return items.length === 1 && items[0].dir ? { item: 0, path: "" } : null;
}

function go(loc) {
  S.loc = loc;
  history.pushState({ loc }, "");
  render();
}

addEventListener("popstate", (e) => {
  if (!S.hello) return;
  S.loc = e.state?.loc ?? rootLoc();
  render();
});

// ── Views ──

function render() {
  if (!S.hello) return;
  if (S.hello.mode === "receive") return renderInbox();
  if (S.loc === null) S.loc = rootLoc();
  const items = S.hello.items;
  if (!S.loc) {
    if (items.length === 1) return renderFile(items[0]);
    return renderList(null, "Shared with you", items.map((it, i) => ({ ...it, item: i, path: "" })));
  }
  renderFolder(S.loc);
}

function renderFile(it) {
  const key = "0:";
  const dl = S.downloads.get(key) || makeDownload(key, it.name);
  const btn = h("button", { class: "primary" });
  const bar = progressBar();
  const preview = h("div", { class: "bigicon" }, icon(it.name, false));
  if (thumbable(it.name)) wantThumb(preview, 0, "", 480, true);
  const card = h("section", { class: "card single" },
    preview,
    h("h2", { class: "name" }, display(it.name)),
    h("p", { class: "meta" }, fmtSize(it.size)),
    bar.el,
    btn,
  );
  const update = () => {
    bar.set(dl);
    btn.textContent = dl.state === "loading" ? "Receiving…" : dl.state === "done" ? "Save again" : "Download";
    btn.disabled = dl.state === "loading";
  };
  dl.listeners.clear();
  dl.listeners.add(update);
  btn.onclick = () => download(key, { t: "get", item: 0, path: "" }, it.name);
  if (S.finished) showBanner("Picked up. Your Mac has stopped sharing.");
  update();
  mount(card);
  // One file was shared: start right away.
  if (!S.autoStarted) {
    S.autoStarted = true;
    btn.click();
  }
}

async function renderFolder(loc) {
  const listKey = `${loc.item}:${loc.path}`;
  const it = S.hello.items[loc.item];
  const title = loc.path ? loc.path.split("/").pop() : it.name;
  const cached = S.lists.get(listKey);
  if (cached) drawFolder(loc, title, cached);
  else showStatus("", "Loading…", { spin: true, inline: true });
  try {
    const entries = await call({ t: "list", item: loc.item, path: loc.path });
    S.lists.set(listKey, entries);
    if (S.loc === loc) drawFolder(loc, title, entries);
  } catch (err) {
    if (S.loc === loc && !cached) toast(err.message);
  }
}

function drawFolder(loc, title, entries) {
  const rows = entries.map((e) => ({ ...e, item: loc.item, path: loc.path ? `${loc.path}/${e.name}` : e.name }));
  renderList(loc, display(title), rows);
}

function renderList(loc, title, rows) {
  rows.sort((a, b) => (b.dir - a.dir) || display(a.name).localeCompare(display(b.name), undefined, { numeric: true, sensitivity: "base" }));
  const canGoUp = loc && !(rootLoc() && loc.item === 0 && loc.path === "");
  const head = h("div", { class: "head" },
    canGoUp ? h("button", { class: "back", "aria-label": "Back", onclick: () => history.back() }, svg(ICONS.back)) : null,
    h("div", { class: "title" },
      h("span", { class: "k" }, "Contents"),
      h("h2", {}, title),
      h("p", { class: "meta" }, plural(rows.length, "item")),
    ),
  );

  const list = h("ul", { class: "files" }, ...rows.map(row));
  if (!rows.length) list.append(h("li", { class: "empty" }, "This folder is empty."));

  const zipReq = loc ? { t: "zip", item: loc.item, path: loc.path } : { t: "zip", item: -1, path: "" };
  const zipKey = `zip:${zipReq.item}:${zipReq.path}`;
  const zipName = loc ? `${title}.zip` : S.hello.zip;
  const label = canGoUp ? "Get this folder (ZIP)" : "Get the whole parcel (ZIP)";
  const zip = downloadButton(zipKey, zipName, () => download(zipKey, zipReq, zipName), label);

  mount(head, list, rows.length ? h("div", { class: "foot" }, zip) : null);
}

function row(e) {
  const key = `${e.item}:${e.path}`;
  const meta = h("span", { class: "meta" });
  const bar = progressBar();
  const status = h("span", { class: "status" });
  const iconEl = h("span", { class: "icon" }, icon(e.name, e.dir));
  if (!e.dir && thumbable(e.name)) wantThumb(iconEl, e.item, e.path, 144);
  const li = h("li", { class: "row" + (e.dir ? " dir" : "") },
    iconEl,
    h("span", { class: "text" }, h("span", { class: "name" }, display(e.name)), meta, bar.el),
    status,
  );
  if (e.dir) {
    meta.textContent = e.mtime ? fmtDate(e.mtime) : "";
    status.append(svg(ICONS.chevron));
    li.onclick = () => go({ item: e.item, path: e.path });
    return li;
  }
  const dl = S.downloads.get(key) || makeDownload(key, e.name);
  const update = () => {
    bar.set(dl);
    meta.textContent = dl.state === "loading"
      ? `${fmtSize(dl.received)} of ${fmtSize(dl.size || e.size)}`
      : [fmtSize(e.size), e.mtime && fmtDate(e.mtime)].filter(Boolean).join(" · ");
    status.replaceChildren(dl.state === "done" ? svg(ICONS.check) : dl.state === "loading" ? "" : svg(ICONS.download));
    li.classList.toggle("done", dl.state === "done");
  };
  dl.listeners.clear();
  dl.listeners.add(update);
  update();
  li.onclick = () => download(key, { t: "get", item: e.item, path: e.path }, e.name);
  return li;
}

// ── Drop-off (receive mode): the phone sends files to a folder on the Mac ──

const FRAME = 64 << 10;     // bytes per message, like the Mac uses
const READ = 1 << 20;       // bytes read from the file at a time
const HIGH = 4 << 20;       // pause sending above this many buffered bytes
const LOW = 1 << 20;

const picker = h("input", { type: "file", multiple: true, hidden: true });
picker.onchange = () => {
  for (const file of picker.files) addUpload(file);
  picker.value = "";
  if (S.pumping && S.dc?.readyState === "open") send({ t: "run", run: runInfo() });
  pump();
};
document.body.append(picker);

// A run is all files sent since the queue was last empty. The Mac shows its
// total progress. Failed files leave the run, so it can still reach 100%.
function runInfo() {
  const r = S.run;
  return r && { files: r.files, bytes: r.bytes, done: r.done, doneBytes: r.doneBytes };
}

function joinRun(up) {
  if (!S.uploads.some((u) => u !== up && (u.state === "queued" || u.state === "sending" || u.state === "saving"))) {
    S.run = { files: 0, bytes: 0, done: 0, doneBytes: 0 };
  }
  S.run.files++;
  S.run.bytes += up.file.size;
}

function leaveRun(up, delivered) {
  if (!S.run) return;
  if (delivered) {
    S.run.done++;
    S.run.doneBytes += up.file.size;
  } else {
    S.run.files--;
    S.run.bytes -= up.file.size;
  }
}

function addUpload(file) {
  const up = {
    file, state: "queued", sent: 0, error: null, savedAs: null, preview: null, listeners: new Set(),
    notify() { for (const f of up.listeners) f(up); },
  };
  if (/^image\//.test(file.type)) up.preview = URL.createObjectURL(file);
  joinRun(up);
  S.uploads.push(up);
  if (S.hello?.mode === "receive") renderInbox();
}

// Send queued files one at a time.
async function pump() {
  if (S.pumping) return;
  S.pumping = true;
  try {
    let up;
    while ((up = S.uploads.find((u) => u.state === "queued")) && S.dc?.readyState === "open" && S.hello) {
      await sendFile(up);
    }
  } finally {
    S.pumping = false;
  }
}

async function sendFile(up) {
  const dc = S.dc;
  const id = S.nextId++;
  const { file } = up;
  up.state = "sending";
  up.sent = 0;
  up.error = null;
  S.sending.set(id, up);
  up.notify();
  dc.bufferedAmountLowThreshold = LOW;
  try {
    send({ t: "put", id, name: file.name, size: file.size, mtime: file.lastModified || 0, run: runInfo() });
    for (let off = 0; off < file.size; ) {
      const buf = new Uint8Array(await file.slice(off, off + READ).arrayBuffer());
      for (let i = 0; i < buf.length; i += FRAME - 4) {
        if (S.dc !== dc || up.state !== "sending") return;
        if (dc.bufferedAmount > HIGH) await drained(dc);
        if (S.dc !== dc || up.state !== "sending") return;
        const part = buf.subarray(i, i + FRAME - 4);
        const frame = new Uint8Array(4 + part.length);
        new DataView(frame.buffer).setUint32(0, id);
        frame.set(part, 4);
        dc.send(frame);
        off += part.length;
        up.sent = Math.max(0, off - dc.bufferedAmount);
        up.notify();
      }
    }
    send({ t: "done", id });
    up.sent = file.size;
    up.state = "saving";
    up.notify();
    await new Promise((res) => (up.settle = res)); // "saved", "error" or connection lost
  } catch (err) {
    if (S.sending.get(id) === up) { S.sending.delete(id); send({ t: "cancel", id }); }
    upFail(up, err.message || "Could not read the file");
  }
}

function drained(dc) {
  return new Promise((res) => {
    const done = () => { dc.removeEventListener("bufferedamountlow", done); dc.removeEventListener("close", done); res(); };
    dc.addEventListener("bufferedamountlow", done);
    dc.addEventListener("close", done);
  });
}

function upFail(up, msg) {
  if (up.state === "done" || up.state === "error") return;
  leaveRun(up, false);
  up.state = "error";
  up.error = msg;
  up.notify();
  up.settle?.();
}

function renderInbox() {
  const ups = S.uploads;
  const done = ups.filter((u) => u.state === "done").length;
  const head = h("div", { class: "head" },
    h("div", { class: "title" },
      h("span", { class: "k" }, "Drop-off folder"),
      h("h2", {}, display(S.hello.folder || "")),
      h("p", { class: "meta" }, ups.length
        ? `${done} of ${plural(ups.length, "file")} delivered`
        : "Files you send go into this folder on the Mac."),
    ),
  );
  const list = h("ul", { class: "files" }, ...ups.map(upRow));
  if (!ups.length) list.append(h("li", { class: "empty" }, "Nothing sent yet."));
  const btn = h("button", { class: "primary", onclick: () => picker.click() }, svg(ICONS.upload), "Choose files to send");
  mount(head, list, h("div", { class: "foot" }, btn));
}

function upRow(up) {
  const name = display(up.file.name);
  const meta = h("span", { class: "meta" });
  const bar = progressBar();
  const status = h("span", { class: "status" });
  const iconEl = h("span", { class: "icon" }, icon(name, false));
  if (up.preview) {
    const img = h("img", { src: up.preview, alt: "", decoding: "async" });
    img.onload = () => { iconEl.replaceChildren(img); iconEl.classList.add("thumb"); };
  }
  const nameEl = h("span", { class: "name" }, name);
  const li = h("li", { class: "row" }, iconEl, h("span", { class: "text" }, nameEl, meta, bar.el), status);
  const update = () => {
    bar.set({ state: up.state === "sending" ? "loading" : "", received: up.sent, size: up.file.size });
    meta.textContent =
      up.state === "sending" ? `${fmtSize(up.sent)} of ${fmtSize(up.file.size)}` :
      up.state === "queued" ? `${fmtSize(up.file.size)} · Waiting` :
      up.state === "saving" ? "Saving…" :
      up.state === "error" ? `${up.error} · Tap to try again` :
      up.savedAs && up.savedAs !== up.file.name.normalize("NFC") ? `Saved as ${display(up.savedAs)}` : `${fmtSize(up.file.size)} · Delivered`;
    nameEl.classList.toggle("err", up.state === "error");
    status.replaceChildren(up.state === "done" ? svg(ICONS.check) : up.state === "error" ? svg(ICONS.retry) : "");
    li.classList.toggle("done", up.state === "done");
    const head = app.querySelector(".head .meta");
    if (head && S.uploads.length) renderInboxMeta(head);
  };
  up.listeners.clear();
  up.listeners.add(update);
  li.onclick = () => {
    if (up.state !== "error") return;
    joinRun(up);
    up.state = "queued";
    up.notify();
    pump();
  };
  update();
  return li;
}

function renderInboxMeta(el) {
  const done = S.uploads.filter((u) => u.state === "done").length;
  el.textContent = `${done} of ${plural(S.uploads.length, "file")} delivered`;
}

// Leaving the page stops the uploads.
addEventListener("beforeunload", (e) => {
  if (S.uploads.some((u) => u.state === "sending" || u.state === "queued" || u.state === "saving")) e.preventDefault();
});

function downloadButton(key, name, start, label) {
  const dl = S.downloads.get(key) || makeDownload(key, name);
  const btn = h("button", { class: "primary" });
  const bar = progressBar();
  const update = () => {
    bar.set(dl);
    const text = dl.state === "loading" ? `Packing… ${fmtSize(dl.received)}` : dl.state === "done" ? "Save ZIP again" : label;
    btn.replaceChildren(svg(ICONS.parcel), text);
    btn.disabled = dl.state === "loading";
  };
  dl.listeners.clear();
  dl.listeners.add(update);
  btn.onclick = start;
  update();
  return h("div", { class: "zip" }, bar.el, btn);
}

function progressBar() {
  const fill = h("span");
  const el = h("span", { class: "bar", hidden: true }, fill);
  return {
    el,
    set(dl) {
      el.hidden = dl.state !== "loading";
      const total = dl.size || 0;
      const p = total ? Math.min(dl.received / total, dl.approx ? 0.99 : 1) : 0;
      fill.style.width = `${(p * 100).toFixed(1)}%`;
    },
  };
}

function showStatus(title, text, opts = {}) {
  const parts = [];
  if (opts.spin) parts.push(h("div", { class: "spinner" }));
  if (title) parts.push(h("h2", {}, title));
  parts.push(h("p", { class: "meta" }, text));
  if (opts.retry) parts.push(h("button", { class: "primary", onclick: connect }, "Reconnect"));
  mount(h("section", { class: "card status" }, ...parts));
}

function mount(...nodes) {
  app.replaceChildren(...nodes.filter(Boolean));
}

let toastTimer;
function toast(text) {
  toastEl.textContent = text;
  toastEl.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (toastEl.hidden = true), 4000);
}

// ── Helpers ──

function h(tag, attrs = {}, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k.startsWith("on")) el[k] = v;
    else if (v === true) el.setAttribute(k, "");
    else if (v != null && v !== false) el.setAttribute(k, v);
  }
  el.append(...kids.filter((k) => k != null));
  return el;
}

const display = (name) => String(name).normalize("NFC");

function fmtSize(n) {
  if (n < 1000) return `${n} B`;
  const u = ["kB", "MB", "GB", "TB"];
  let i = -1;
  do { n /= 1000; i++; } while (n >= 1000 && i < u.length - 1);
  return `${n.toFixed(n < 10 ? 1 : 0)} ${u[i]}`;
}

function fmtDate(ms) {
  return new Date(ms).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}

const plural = (n, w) => `${n} ${w}${n === 1 ? "" : "s"}`;

const ICONS = {
  folder: "M3 6.5A1.5 1.5 0 0 1 4.5 5h4.6l2 2h8.4A1.5 1.5 0 0 1 21 8.5v9a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5z",
  file: "M6 3h8l5 5v12a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1zm8 0v5h5",
  image: "M4 5h16v14H4zM4 16l5-5 4 4 2-2 5 5M15.5 9.5a1.5 1.5 0 1 1 0-.01",
  video: "M4 6h12v12H4zM16 10l4-2.5v9L16 14",
  audio: "M9 18V6l10-2v12M9 18a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0zm10-2a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0z",
  archive: "M4 4h16v4H4zM5 8v12h14V8M10 12h4",
  doc: "M6 3h8l5 5v12a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1zm8 0v5h5M8 13h8M8 17h6",
  back: "M15 5l-7 7 7 7",
  chevron: "M9 5l7 7-7 7",
  check: "M5 12.5l4.5 4.5L19 7.5",
  download: "M12 4v11m0 0l-4.5-4.5M12 15l4.5-4.5M5 19h14",
  parcel: "M3 8l9-5 9 5v8l-9 5-9-5zM3 8l9 5 9-5M12 13v8",
  upload: "M12 15V4m0 0L7.5 8.5M12 4l4.5 4.5M5 19h14",
  retry: "M4 12a8 8 0 1 0 2.5-5.8M4 4v4.5h4.5",
};

const KINDS = {
  image: /\.(jpe?g|png|gif|webp|heic|heif|avif|bmp|tiff?|svg)$/i,
  video: /\.(mp4|mov|m4v|mkv|webm|avi)$/i,
  audio: /\.(mp3|m4a|aac|wav|flac|ogg|opus)$/i,
  archive: /\.(zip|rar|7z|tar|gz|tgz|bz2|xz|apk|dmg)$/i,
  doc: /\.(pdf|txt|md|rtf|docx?|xlsx?|pptx?|pages|numbers|key|csv|json)$/i,
};

function icon(name, dir) {
  if (dir) return svg(ICONS.folder, "folder");
  for (const [k, re] of Object.entries(KINDS)) if (re.test(name)) return svg(ICONS[k], k);
  return svg(ICONS.file, "file");
}

function svg(d, cls = "") {
  const NS = "http://www.w3.org/2000/svg";
  const s = document.createElementNS(NS, "svg");
  s.setAttribute("viewBox", "0 0 24 24");
  s.setAttribute("aria-hidden", "true");
  if (cls) s.setAttribute("class", cls);
  const p = document.createElementNS(NS, "path");
  p.setAttribute("d", d);
  s.append(p);
  return s;
}

// After a reload, open the same folder again.
S.loc = history.state?.loc ?? null;
if (!history.state) history.replaceState({ loc: null }, "");
connect();
