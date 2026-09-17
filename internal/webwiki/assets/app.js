/*
  gwiki browser view.

  One page, no framework and no build step. Every view is a fetch and a
  redraw: holding a local copy in step with a wiki that four front ends write
  to is exactly the class of bug that avoids.

  The token comes from the address the command printed and travels in the
  Authorization header, so it never reaches another origin.
*/

const token = new URLSearchParams(location.search).get("token") || "";
const main = document.getElementById("main");
const tree = document.getElementById("tree");
const statusLine = document.getElementById("status");

let pages = [];
let version = 0;
let editing = null; // {page, base} while the editor is open

// ---------------------------------------------------------------- transport

async function api(path, options = {}) {
  const headers = Object.assign({ Authorization: "Bearer " + token }, options.headers || {});
  if (options.body) headers["Content-Type"] = "application/json";
  const res = await fetch(path, Object.assign({}, options, { headers }));
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch (e) { data = { error: text }; }
  if (!res.ok) {
    const err = new Error((data && data.error) || res.statusText);
    err.status = res.status;
    err.data = data;
    throw err;
  }
  return data;
}

function say(text, isError) {
  statusLine.textContent = text || "";
  statusLine.className = isError ? "error" : "";
  if (text && !isError) setTimeout(() => { if (statusLine.textContent === text) say(""); }, 4000);
}

// ---------------------------------------------------------------- helpers

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined) continue;
    node.append(c.nodeType ? c : document.createTextNode(String(c)));
  }
  return node;
}

const pageHref = (p) => "#/page/" + encodeURIComponent(p);

function titleOf(path) {
  const found = pages.find((p) => p.path === path);
  return found ? found.title : path;
}

function ago(iso) {
  const then = new Date(iso).getTime();
  if (!then) return "";
  const mins = Math.floor((Date.now() - then) / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return mins + "m ago";
  if (mins < 60 * 24) return Math.floor(mins / 60) + "h ago";
  if (mins < 60 * 24 * 30) return Math.floor(mins / 1440) + "d ago";
  return iso.slice(0, 10);
}

// inlineText shows links as the text they display: [[target|label]] as label,
// [[target]] as target, and [text](dest) as text. It follows inlineText in
// internal/tui, so both interfaces show a task alike.
function inlineText(s) {
  return String(s)
    .replace(/\[\[(?:[^\]|]*\|)?([^\]]*)\]\]/g, "$1")
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1");
}

// plainSnippet takes the markdown syntax out of a search snippet, which is page
// source, keeping the match markers. It follows snippet in internal/tui.
function plainSnippet(text) {
  return inlineText(text)
    .replace(/(^|\s)(?:[-*+]|\d+[.)])\s(?:\[[ xX]\]\s)?/g, "$1")
    .replace(/(^|\s)#{1,6}\s/g, "$1")
    .replace(/```\w*|-{3,}|[|`]|\*\*|__/g, " ")
    .split(/\s+/).filter(Boolean).join(" ");
}

// snippet turns the search markers into highlighted text, escaping the rest.
function snippet(text) {
  const out = el("span", { class: "snippet" });
  for (const part of plainSnippet(text).split("\u0002")) {
    const cut = part.indexOf("\u0003");
    if (cut < 0) { out.append(part); continue; }
    out.append(el("mark", { text: part.slice(0, cut) }), part.slice(cut + 1));
  }
  return out;
}

// ---------------------------------------------------------------- the tree

async function loadTree() {
  const res = await api("/api/pages");
  pages = res.pages;
  document.getElementById("project").textContent = res.name || "gwiki";
  document.title = (res.name || "gwiki") + " wiki";
  drawTree();
}

function drawTree() {
  tree.replaceChildren();
  const here = current().kind === "page" ? current().arg : "";
  // A directory's README is its page: the directory's heading links to it.
  const readme = new Map();
  for (const p of pages) {
    const at = p.path.lastIndexOf("/");
    if (at > 0 && p.path.slice(at + 1).toLowerCase() === "readme") readme.set(p.path.slice(0, at), p);
  }
  let dir = null;
  for (const p of pages) {
    const at = p.path.lastIndexOf("/");
    const group = at < 0 ? "" : p.path.slice(0, at);
    if (group !== dir) {
      dir = group;
      const page = readme.get(group);
      if (page) {
        tree.append(el("a", {
          href: pageHref(page.path),
          class: "dir" + (page.path === here ? " here" : ""),
          title: page.path,
          text: group + "/ · " + (page.title || group),
        }));
      } else if (group) {
        tree.append(el("div", { class: "dir", text: group + "/" }));
      }
    }
    if (readme.get(group) === p) continue;
    const link = el("a", {
      href: pageHref(p.path),
      class: (p.path === here ? "here " : "") + (group ? "nest" : ""),
      title: p.path,
      text: p.title || p.path,
    });
    tree.append(link);
  }
}

// ---------------------------------------------------------------- routing

function current() {
  const hash = location.hash.replace(/^#\/?/, "");
  const [kind, ...rest] = hash.split("/");
  const arg = decodeURIComponent(rest.join("/"));
  return { kind: kind || "", arg };
}

async function route() {
  const { kind, arg } = current();
  editing = null;
  try {
    switch (kind) {
      case "page": await showPage(arg); break;
      case "file": await showFile(arg); break;
      case "broken": await showBroken(); break;
      case "tasks": await showTasks(); break;
      case "search": await showSearch(arg); break;
      case "orphans": await showHealth("orphans"); break;
      case "deadends": await showHealth("deadEnds"); break;
      case "tag": await showList("pages tagged #" + arg, (await api("/api/pages?tag=" + encodeURIComponent(arg))).pages); break;
      case "dir": await showList("pages in " + arg + "/", (await api("/api/pages?dir=" + encodeURIComponent(arg))).pages); break;
      default: await showOverview();
    }
  } catch (err) {
    main.replaceChildren(el("h1", { text: "not found" }), el("p", { text: err.message }));
    say(err.message, true);
  }
  drawTree();
}

// ---------------------------------------------------------------- overview

async function showOverview() {
  const o = await api("/api/overview");

  const recent = el("ul", {}, ...o.recent.map((c) =>
    el("li", {},
      el("a", { href: pageHref(c.path), text: c.title || c.path }),
      el("span", { class: "when" },
        ago(c.modified) + (c.author ? "  " + c.author : "") + (c.uncommitted ? "  uncommitted" : "")))));

  const tasks = el("ul", {}, ...o.tasks.slice(0, 10).map((t) => taskRow(t, o)));

  const count = (label, n, href) =>
    el("li", {}, el("a", { href, text: label }), el("span", { class: "count" + (n ? " bad" : ""), text: String(n) }));
  const health = el("ul", {},
    count("broken links", o.broken, "#/broken"),
    count("orphan pages", o.orphans.length, "#/orphans"),
    count("dead ends", o.deadEnds.length, "#/deadends"));

  const structure = el("ul", {},
    ...o.dirs.slice(0, 6).map((d) =>
      el("li", {}, el("a", { href: "#/dir/" + encodeURIComponent(d.name), text: (d.name || "(top)") + "/" }),
        el("span", { class: "when", text: String(d.pages) }))),
    ...o.tags.slice(0, 6).map((t) =>
      el("li", {}, el("a", { href: "#/tag/" + encodeURIComponent(t.name), class: "tag", text: "#" + t.name }),
        el("span", { class: "when", text: String(t.pages) }))),
    ...o.hubs.slice(0, 6).map((h) =>
      el("li", {}, el("a", { href: pageHref(h.name), text: o.titles[h.name] || h.name }),
        el("span", { class: "when", text: "<- " + h.pages }))));

  let taskLabel = o.tasks.length + " open";
  if (o.overdue) taskLabel += ", " + o.overdue + " overdue";
  if (o.dueSoon) taskLabel += ", " + o.dueSoon + " due within a week";

  main.replaceChildren(
    el("h1", { text: "Overview" }),
    el("p", { class: "meta", text: o.pages + " pages" }),
    el("div", { class: "cards" },
      el("section", { class: "card" }, el("h2", { text: "Recent changes" }), recent),
      el("section", { class: "card" }, el("h2", { text: "Tasks" }), el("p", { class: "meta", text: taskLabel }), tasks),
      el("section", { class: "card" }, el("h2", { text: "Health" }), health),
      el("section", { class: "card" }, el("h2", { text: "Structure" }), structure)));

}

// showHealth lists the orphan or dead-end pages, which the overview carries.
async function showHealth(which) {
  const o = await api("/api/overview");
  const titles = { orphans: "orphan pages: nothing links to them", deadEnds: "dead ends: they link to no page" };
  await showList(titles[which], o[which] || []);
}

function taskRow(t, o) {
  const where = t.line ? t.page + ":" + t.line : t.page;
  const due = t.due
    ? el("span", { class: new Date(t.due) < new Date(new Date().toDateString()) ? "overdue" : "due", text: (t.due < today() ? "overdue " : "due ") + t.due })
    : null;
  const box = el("input", { type: "checkbox", title: "mark done" });
  box.checked = t.status === "done";
  box.addEventListener("change", () => setTask(t, box.checked ? "done" : "open"));
  return el("li", {},
    el("span", {}, t.line ? box : null, " ",
      el("a", { href: pageHref(t.page), text: inlineText(t.text) })),
    el("span", { class: "when" }, due || where));
}

const today = () => new Date().toISOString().slice(0, 10);

async function setTask(t, status) {
  try {
    await api("/api/task", { method: "POST", body: JSON.stringify({ page: t.page, line: t.line, text: t.text, status }) });
    say(inlineText(t.text) + ": " + status);
    route();
  } catch (err) {
    say(err.message, true);
    route();
  }
}

// ---------------------------------------------------------------- pages

async function showPage(id) {
  const p = await api("/api/page?p=" + encodeURIComponent(id));
  document.title = (p.title || p.path) + " - gwiki";

  const body = el("div", { class: "rendered" });
  body.innerHTML = p.html;
  // The title is above; a first heading repeating it would say it twice.
  const first = body.firstElementChild;
  if (first && first.tagName === "H1" && first.textContent.trim() === (p.title || "").trim()) first.remove();
  body.querySelectorAll("a[href^='http']").forEach((a) => {
    a.target = "_blank";
    a.rel = "noreferrer noopener";
  });
  body.querySelectorAll("input[type=checkbox]").forEach((box, i) => {
    const task = p.tasks.filter((t) => t.line > 0)[i];
    if (!task) return;
    box.disabled = false;
    box.addEventListener("change", () => setTask(task, box.checked ? "done" : "open"));
  });

  const meta = [p.path];
  if (p.type === "task") meta.push("task: " + (p.status || "open"));
  if (p.due) meta.push("due " + p.due);

  main.replaceChildren(
    el("h1", { text: p.title || p.path }),
    el("p", { class: "meta" }, meta.join("  -  "), " ",
      ...(p.tags || []).map((t) => el("a", { class: "tag", href: "#/tag/" + encodeURIComponent(t), text: "#" + t })),
      " ",
      el("button", { class: "action", onclick: () => openEditor(p) }, "edit")),
    body,
    linkPanel(p));

  const anchor = location.hash.split("#")[2];
  if (anchor) {
    const target = body.querySelector("#" + CSS.escape(anchor));
    if (target) target.scrollIntoView();
  } else {
    main.scrollTop = 0;
  }
}

function linkPanel(p) {
  const list = (title, links, showPage) => {
    if (!links.length) return null;
    return el("div", {},
      el("h3", { text: title + " (" + links.length + ")" }),
      el("ul", {}, ...links.map((l) =>
        el("li", {},
          el("a", { href: l.href || "#/", text: (showPage ? l.page + ":" + l.line + "  " : "") + l.written }),
          l.status !== "ok" ? el("span", { class: "status", text: "  " + l.status }) : null))));
  };
  const broken = p.links.filter((l) => l.status !== "ok");
  return el("div", { class: "panel" },
    list("broken links", broken, false),
    list("links", p.links.filter((l) => l.status === "ok"), false),
    list("backlinks", p.backlinks, true));
}

async function showFile(arg) {
  const [file, anchor] = [arg, location.hash.split("#")[2] || ""];
  const f = await api("/api/file?p=" + encodeURIComponent(file));
  const lines = f.text.replace(/\n$/, "").split("\n");
  const range = /^L(\d+)(?:-L(\d+))?$/.exec(anchor);
  const from = range ? Number(range[1]) : 0;
  const to = range ? Number(range[2] || range[1]) : 0;

  const box = el("div", { class: "filelines" }, ...lines.map((line, i) =>
    el("div", { class: i + 1 >= from && i + 1 <= to ? "hit" : "" }, line)));
  main.replaceChildren(el("h1", { text: f.path }), el("p", { class: "meta", text: "a file in the repository" }), box);
  const hit = box.querySelector(".hit");
  if (hit) hit.scrollIntoView({ block: "center" });
}

async function showBroken() {
  const links = await api("/api/check");
  main.replaceChildren(
    el("h1", { text: "Broken links" }),
    el("p", { class: "meta", text: links.length + " to fix; gwiki check --fix repairs them" }),
    el("ul", { class: "rows" }, ...links.map((l) =>
      el("li", {},
        el("a", { class: "where", href: pageHref(l.page), text: l.page + ":" + l.line }),
        el("span", { text: l.written }),
        el("span", { class: "status", text: l.status })))));
}

async function showTasks() {
  const o = await api("/api/overview");
  main.replaceChildren(
    el("h1", { text: "Tasks" }),
    el("p", { class: "meta", text: o.tasks.length + " open, " + o.overdue + " overdue" }),
    el("ul", { class: "rows" }, ...o.tasks.map((t) => taskRow(t, o))));
}

async function showSearch(query) {
  document.getElementById("search").value = query;
  const hits = await api("/api/search?q=" + encodeURIComponent(query));
  main.replaceChildren(
    el("h1", { text: "Search" }),
    el("p", { class: "meta", text: hits.length + " pages match " + JSON.stringify(query) }),
    el("ul", { class: "rows" }, ...hits.map((h) =>
      el("li", {},
        el("span", {}, el("a", { href: pageHref(h.path), text: h.title || h.path }), " ", snippet(h.snippet))))));
}

async function showList(title, items) {
  main.replaceChildren(
    el("h1", { text: title }),
    el("p", { class: "meta", text: items.length + " pages" }),
    el("ul", { class: "rows" }, ...items.map((p) =>
      el("li", {}, el("a", { href: pageHref(p.path), text: p.title || p.path }), el("span", { class: "where", text: p.path })))));
}

// ---------------------------------------------------------------- editing

function openEditor(p) {
  editing = { page: p.path, base: p.hash };
  const area = el("textarea", { spellcheck: "false" });
  area.value = p.source;
  area.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter" && !ev.shiftKey) continueList(ev, area);
    if (ev.key === "s" && (ev.metaKey || ev.ctrlKey)) { ev.preventDefault(); save(area); }
    if (ev.key === "Escape") location.hash = pageHref(p.path).slice(1);
  });

  main.replaceChildren(
    el("h1", { text: "Editing " + (p.title || p.path) }),
    el("div", { id: "editor" },
      area,
      el("div", { class: "bar" },
        el("button", { class: "action", onclick: () => save(area) }, "save"),
        el("button", { class: "action", onclick: () => { location.hash = pageHref(p.path).slice(1); } }, "cancel"),
        el("span", { class: "hint", text: "ctrl-s saves, esc leaves, enter continues a list" }))));
  area.focus();
}

async function save(area) {
  if (!editing) return;
  try {
    const res = await api("/api/save", {
      method: "POST",
      body: JSON.stringify({ page: editing.page, base: editing.base, text: area.value }),
    });
    editing.base = res.hash;
    say("saved " + editing.page);
    location.hash = pageHref(editing.page).slice(1);
  } catch (err) {
    if (err.status === 409) {
      say(err.message + " - copy your text, then reload", true);
      return;
    }
    say(err.message, true);
  }
}

// continueList repeats a markdown list marker on Enter, as the terminal
// editor does. An item with no text ends the list.
const listRe = /^(\s*)(>\s?|[-*+] \[[ xX]\] |[-*+] |(\d+)([.)]) )(\s*)/;

function continueList(ev, area) {
  const value = area.value;
  const at = area.selectionStart;
  if (at !== area.selectionEnd) return;
  const lineStart = value.lastIndexOf("\n", at - 1) + 1;
  const line = value.slice(lineStart, at);
  const m = listRe.exec(line);
  if (!m) return;

  const rest = line.slice(m[0].length);
  ev.preventDefault();
  if (rest.trim() === "") {
    // An empty item: drop the marker and leave a blank line.
    replace(area, lineStart, at, "");
    return;
  }
  let marker = m[2];
  if (m[3]) marker = String(Number(m[3]) + 1) + m[4] + " ";
  marker = marker.replace(/\[[xX]\]/, "[ ]");
  replace(area, at, at, "\n" + m[1] + marker);
}

function replace(area, from, to, text) {
  area.setRangeText(text, from, to, "end");
  area.dispatchEvent(new Event("input"));
}

async function newPage() {
  const title = prompt("title of the new page");
  if (!title) return;
  const dir = current().kind === "page" && current().arg.includes("/")
    ? current().arg.slice(0, current().arg.lastIndexOf("/"))
    : "";
  try {
    const info = await api("/api/new", { method: "POST", body: JSON.stringify({ title, dir, task: false }) });
    await loadTree();
    location.hash = pageHref(info.path).slice(1);
    say("created " + info.path);
  } catch (err) {
    say(err.message, true);
  }
}

// ---------------------------------------------------------------- events

function watch() {
  const stream = new EventSource("/api/events?token=" + encodeURIComponent(token));
  stream.onmessage = (ev) => {
    const v = Number(ev.data);
    if (version && v !== version && !editing) {
      loadTree();
      route();
    }
    version = v;
  };
  stream.onerror = () => say("the connection to gwiki dropped; it will retry", true);
}

document.getElementById("search").addEventListener("keydown", (ev) => {
  if (ev.key !== "Enter") return;
  const q = ev.target.value.trim();
  location.hash = q ? "#/search/" + encodeURIComponent(q) : "#/";
});
document.getElementById("new").addEventListener("click", newPage);

// The theme is in a cookie rather than localStorage: serve picks a new port
// each run, which is a new origin for storage, while a cookie is kept per host.
const themeSelect = document.getElementById("theme");
themeSelect.value = document.documentElement.dataset.theme || "";
themeSelect.addEventListener("change", () => {
  const theme = themeSelect.value;
  if (theme) {
    document.documentElement.dataset.theme = theme;
    document.cookie = "gwiki-theme=" + theme + "; Path=/; Max-Age=31536000; SameSite=Strict";
  } else {
    delete document.documentElement.dataset.theme;
    document.cookie = "gwiki-theme=; Path=/; Max-Age=0; SameSite=Strict";
  }
});

document.addEventListener("keydown", (ev) => {
  const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName);
  if (typing || ev.metaKey || ev.ctrlKey || ev.altKey) return;
  if (ev.key === "/") { ev.preventDefault(); document.getElementById("search").focus(); return; }
  const target = document.querySelector("nav a[data-key='" + ev.key + "']");
  if (target) location.hash = target.getAttribute("href").slice(1);
});

window.addEventListener("hashchange", route);

loadTree().then(route).then(watch).catch((err) => say(err.message, true));
