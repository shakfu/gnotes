/*
  gnotes web view.

  The page holds no model of its own. Every change is a request, and the server
  answers with the new state, which is then rendered from scratch. Keeping a
  local copy in step with an append-only log written by three front ends and by
  other machines is exactly the bug this avoids.
*/

// The token travels in the URL the program printed. Reading it from there and
// sending it as a header means a page on another origin cannot obtain it, so
// no other site can drive this server.
const TOKEN = new URLSearchParams(location.search).get("token") || "";

const $ = (id) => document.getElementById(id);

const el = {
  project: $("project"), counts: $("counts"), location: $("location"),
  search: $("search"), sort: $("sort"), scope: $("scope"),
  notebooks: $("notebooks"), tags: $("tags"), filters: $("filters"),
  rows: $("rows"), empty: $("empty"),
  detail: $("detail"), detailInner: $("detail-inner"),
  shell: document.querySelector(".shell"), rail: $("rail"),
  toast: $("toast"), ask: $("ask"), askForm: $("ask-form"),
  askLabel: $("ask-label"), askInput: $("ask-input"), askOk: $("ask-ok"),
};

// view is what the page is showing: the query, the filters, the selection.
// It is deliberately the only mutable state here.
const view = {
  notebook: "",
  query: "",
  sort: "rank",
  filters: {},      // kind, status, overdue
  tags: new Set(),
  selected: "",
  entries: [],
};

let lastDeleted = null;   // for the undo offer after a deletion

// notebookNames maps a notebook id to its title, so a row can say where it
// lives without a second lookup per render.
const notebookNames = new Map();

/* ------------------------------------------------------------------ api */

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: {
      "Authorization": `Bearer ${TOKEN}`,
      ...(body ? { "Content-Type": "application/json" } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });

  if (!res.ok) {
    // The server sends a plain message the user can act on, so it is shown
    // verbatim rather than translated into something vaguer.
    let message = `${res.status} ${res.statusText}`;
    try {
      const data = await res.json();
      if (data.error) message = data.error;
    } catch { /* a non-JSON error body, such as the auth failure */ }
    throw new Error(message);
  }
  return res.status === 204 ? null : res.json();
}

/* ---------------------------------------------------------------- toast */

let toastTimer;

function toast(message, { error = false, action } = {}) {
  clearTimeout(toastTimer);
  el.toast.textContent = message;
  el.toast.classList.toggle("is-error", error);

  if (action) {
    const button = document.createElement("button");
    button.textContent = action.label;
    button.addEventListener("click", () => {
      el.toast.hidden = true;
      action.run();
    });
    el.toast.append(button);
  }

  el.toast.hidden = false;
  toastTimer = setTimeout(() => { el.toast.hidden = true; }, action ? 9000 : 3500);
}

// run wraps an action so that any failure lands on the toast rather than in
// the console, and the page always refreshes afterwards.
async function run(fn, success) {
  try {
    const result = await fn();
    if (success) toast(success);
    await load();
    return result;
  } catch (err) {
    toast(err.message, { error: true });
    return null;
  }
}

/* ----------------------------------------------------------------- ask */

// ask opens the dialog and resolves with the typed value, or null if
// cancelled. Native <dialog> so escape and focus trapping come from the
// browser rather than being reimplemented.
function ask(label, { value = "", confirm = "Create" } = {}) {
  el.askLabel.textContent = label;
  el.askInput.value = value;
  el.askOk.textContent = confirm;
  el.ask.showModal();
  el.askInput.select();

  return new Promise((resolve) => {
    el.ask.addEventListener("close", () => {
      resolve(el.ask.returnValue === "ok" ? el.askInput.value.trim() : null);
    }, { once: true });
  });
}

/* ---------------------------------------------------------------- load */

function queryString() {
  const params = new URLSearchParams();
  if (view.notebook && !view.query) params.set("notebook", view.notebook);
  if (view.query) params.set("q", view.query);
  if (view.sort !== "rank") params.set("sort", view.sort);

  for (const [key, value] of Object.entries(view.filters)) params.set(key, value);
  for (const tag of view.tags) params.append("tag", tag);

  return params.toString();
}

let loading = null;

async function load() {
  // Requests are coalesced: a burst of keystrokes or a flurry of server events
  // should produce one render, not one per trigger.
  if (loading) return loading;

  loading = (async () => {
    try {
      const state = await api("GET", `/api/state?${queryString()}`);
      render(state);
    } catch (err) {
      toast(err.message, { error: true });
    } finally {
      loading = null;
    }
  })();

  return loading;
}

/* -------------------------------------------------------------- render */

function render(state) {
  view.entries = state.entries;

  el.project.textContent = state.project;
  el.project.title = state.location;
  el.location.textContent = shortPath(state.location);
  el.location.title = state.location;
  document.title = `${state.project} — gnotes`;

  renderCounts(state.counts);
  // Before the rows, which read the notebook names it collects.
  renderNotebooks(state.notebooks);
  renderTags(state.tags);
  renderFilters();
  renderScope(state);
  renderRows(state.entries);

  if (state.problems?.length) {
    toast(`${state.problems.length} event(s) could not be applied`, { error: true });
  }

  // A selection that no longer exists, because it was deleted here or
  // elsewhere, closes rather than lingering as a stale pane.
  if (view.selected && !state.entries.some((n) => n.id === view.selected)) {
    refreshDetail();
  }
}

function renderCounts(c) {
  const parts = [`<span><b>${c.open}</b> open</span>`];
  if (c.doing) parts.push(`<span><b>${c.doing}</b> doing</span>`);
  if (c.overdue) parts.push(`<span class="is-overdue"><b>${c.overdue}</b> overdue</span>`);
  parts.push(`<span><b>${c.notes}</b> notes</span>`);
  el.counts.innerHTML = parts.join("");
}

function renderNotebooks(notebooks) {
  el.notebooks.replaceChildren();

  notebookNames.clear();
  for (const nb of notebooks) notebookNames.set(nb.id, nb.title);

  const add = (id, title, count, open) => {
    const li = document.createElement("li");
    const button = document.createElement("button");
    button.className = "rail__item";
    button.setAttribute("aria-current", String(view.notebook === id));

    const name = document.createElement("span");
    name.className = "rail__name";
    name.textContent = title;
    button.append(name);

    if (count !== null) {
      const badge = document.createElement("span");
      badge.className = "rail__count";
      badge.textContent = open > 0 ? `${open}` : `${count}`;
      badge.title = `${count} entries, ${open} open`;
      button.append(badge);
    }

    button.addEventListener("click", () => {
      view.notebook = id;
      view.selected = "";
      closeDetail();
      el.rail.classList.remove("is-open");
      load();
    });

    li.append(button);
    el.notebooks.append(li);
  };

  add("", "All entries", null);
  for (const nb of notebooks) add(nb.id, nb.title, nb.entries, nb.open);
}

function renderTags(tags) {
  el.tags.replaceChildren();

  if (!tags.length) {
    const hint = document.createElement("span");
    hint.className = "eyebrow";
    hint.style.textTransform = "none";
    hint.textContent = "none yet";
    el.tags.append(hint);
    return;
  }

  for (const { tag, count } of tags) {
    const button = document.createElement("button");
    button.className = "chip";
    button.setAttribute("aria-pressed", String(view.tags.has(tag)));
    button.innerHTML = `#${escapeHTML(tag)}<span class="chip__count">${count}</span>`;

    button.addEventListener("click", () => {
      view.tags.has(tag) ? view.tags.delete(tag) : view.tags.add(tag);
      load();
    });
    el.tags.append(button);
  }
}

function renderFilters() {
  for (const chip of el.filters.querySelectorAll(".chip")) {
    const { filter, value } = chip.dataset;
    chip.setAttribute("aria-pressed", String(view.filters[filter] === value));
  }
}

function renderScope(state) {
  if (view.query) {
    el.scope.textContent = `${state.entries.length} matching “${view.query}”`;
    return;
  }
  const nb = state.notebooks.find((n) => n.id === view.notebook);
  const where = nb ? nb.title : "All entries";
  const narrowed = Object.keys(view.filters).length + view.tags.size;
  el.scope.textContent = narrowed ? `${where} · ${state.entries.length} shown` : where;
}

function renderRows(entries) {
  el.rows.replaceChildren();

  if (!entries.length) {
    el.empty.hidden = false;
    el.empty.innerHTML = emptyMessage();
    return;
  }
  el.empty.hidden = true;

  for (const n of entries) el.rows.append(rowFor(n));
}

function emptyMessage() {
  if (view.query) return `Nothing matches “${escapeHTML(view.query)}”.`;
  if (Object.keys(view.filters).length || view.tags.size) {
    return "Nothing matches these filters. Clear one to widen the list.";
  }
  return `This notebook is empty. Press <kbd>n</kbd> for a note or <kbd>t</kbd> for a task.`;
}

function rowFor(n) {
  const li = document.createElement("li");
  li.className = "row";
  li.dataset.id = n.id;
  li.setAttribute("aria-selected", String(view.selected === n.id));
  li.classList.toggle("is-done", n.status === "done");
  li.classList.toggle("is-deleted", !!n.deleted);

  const ref = document.createElement("span");
  ref.className = "ref";
  ref.textContent = n.ref;
  ref.title = "Handle — pass this to the gnotes command line";
  li.append(ref);

  li.append(markFor(n));

  const title = document.createElement("span");
  title.className = "row__title";
  title.textContent = n.title;
  li.append(title);

  const meta = metaFor(n);
  if (meta.childElementCount) li.append(meta);

  if (n.snippet) {
    const snippet = document.createElement("p");
    snippet.className = "row__snippet";
    snippet.textContent = n.snippet;
    li.append(snippet);
  }

  li.addEventListener("click", (event) => {
    if (event.target.closest(".mark")) return;
    select(n.id);
  });

  return li;
}

// markFor draws the status marker, echoing the terminal's [ ] [~] [x].
function markFor(n) {
  if (n.kind !== "task") {
    const rule = document.createElement("span");
    rule.className = "mark mark--note";
    rule.setAttribute("aria-hidden", "true");
    return rule;
  }

  const button = document.createElement("button");
  button.className = `mark mark--${n.status}`;
  button.setAttribute("aria-label", `Mark ${n.status === "done" ? "open" : "done"}: ${n.title}`);
  button.title = n.status;

  button.addEventListener("click", (event) => {
    event.stopPropagation();
    const next = n.status === "done" ? "open" : "done";
    run(() => api("PATCH", `/api/node/${n.id}`, { status: next }));
  });

  return button;
}

function metaFor(n) {
  const meta = document.createElement("span");
  meta.className = "row__meta";

  // Which notebook, but only when the list spans several: inside one notebook
  // the label would be the same on every row and say nothing.
  if (!view.notebook && notebookNames.size > 1) {
    const where = document.createElement("span");
    where.className = "where";
    where.textContent = notebookNames.get(n.notebook) || "";
    if (where.textContent) meta.append(where);
  }

  if (n.priority === "high") {
    const prio = document.createElement("span");
    prio.className = "prio";
    prio.textContent = "!";
    prio.title = "high priority";
    meta.append(prio);
  }
  for (const tag of n.tags) {
    const chip = document.createElement("span");
    chip.className = "tag";
    chip.textContent = `#${tag}`;
    meta.append(chip);
  }
  if (n.due) {
    const due = document.createElement("span");
    due.className = `due${n.overdue ? " is-overdue" : ""}`;
    due.textContent = n.due;
    due.title = n.overdue ? "overdue" : "due";
    meta.append(due);
  }
  for (const who of n.assignees || []) {
    const person = document.createElement("span");
    person.className = "who";
    person.textContent = `@${who}`;
    meta.append(person);
  }
  return meta;
}

/* -------------------------------------------------------------- detail */

async function select(id) {
  view.selected = id;
  for (const row of el.rows.children) {
    row.setAttribute("aria-selected", String(row.dataset.id === id));
  }
  await refreshDetail();
}

function closeDetail() {
  view.selected = "";
  el.detail.hidden = true;
  el.shell.classList.remove("has-detail");
  el.detailInner.replaceChildren();
}

async function refreshDetail() {
  if (!view.selected) return closeDetail();

  let data;
  try {
    data = await api("GET", `/api/node/${view.selected}`);
  } catch {
    // The entry is gone, most likely deleted from another front end.
    return closeDetail();
  }

  el.detail.hidden = false;
  el.shell.classList.add("has-detail");
  el.detailInner.replaceChildren(detailFor(data));

  // A detached element reports a scrollHeight of zero, so the title can only
  // be sized once it is actually in the document.
  const title = el.detailInner.querySelector(".detail__title");
  if (title) autoGrow(title);
}

function detailFor({ node, path, backlinks, history }) {
  const frag = document.createDocumentFragment();

  // Header: the handle, then a close control.
  const head = document.createElement("div");
  head.className = "detail__head";

  const ref = document.createElement("span");
  ref.className = "ref";
  ref.textContent = node.ref;
  ref.title = "Handle — pass this to the gnotes command line";
  head.append(ref);

  head.append(
    iconButton("Open in list", "↕", () => {
      document.querySelector(`.row[data-id="${node.id}"]`)?.scrollIntoView({ block: "center" });
    }),
    iconButton("Close", "✕", closeDetail),
  );
  frag.append(head);

  // Title, edited in place.
  const title = document.createElement("textarea");
  title.className = "detail__title";
  title.rows = 1;
  title.value = node.title;
  title.setAttribute("aria-label", "Title");

  title.addEventListener("input", () => autoGrow(title));
  title.addEventListener("keydown", (event) => {
    if (event.key === "Enter") { event.preventDefault(); title.blur(); }
    if (event.key === "Escape") { title.value = node.title; title.blur(); }
  });
  title.addEventListener("blur", () => {
    const next = title.value.trim();
    if (!next || next === node.title) { title.value = node.title; return; }
    run(() => api("PATCH", `/api/node/${node.id}`, { title: next }));
  });
  frag.append(title);

  const where = document.createElement("span");
  where.className = "eyebrow detail__path";
  where.textContent = path.join("  /  ");
  frag.append(where);

  frag.append(fieldsFor(node));
  frag.append(bodyFor(node));

  if (node.links?.length) {
    frag.append(section("References", linkList(node.links, node.id)));
  }
  if (backlinks?.length) {
    frag.append(section("Referenced by", linkList(backlinks)));
  }
  if (history?.length) {
    frag.append(section(`History · ${history.length} events`, historyList(history)));
  }

  frag.append(footFor(node));
  return frag;
}

function fieldsFor(node) {
  const grid = document.createElement("div");
  grid.className = "detail__fields";

  const row = (label, control) => {
    const tag = document.createElement("span");
    tag.className = "eyebrow";
    tag.textContent = label;
    grid.append(tag, control);
  };

  if (node.kind === "task") {
    row("Status", selectControl(["open", "doing", "done"], node.status, (value) =>
      api("PATCH", `/api/node/${node.id}`, { status: value })));

    row("Priority", selectControl(["none", "low", "normal", "high"], node.priority || "none", (value) =>
      api("PATCH", `/api/node/${node.id}`, { priority: value === "none" ? "" : value })));

    const due = document.createElement("input");
    due.type = "text";
    due.className = "field-control";
    due.value = node.due || "";
    due.placeholder = "2026-09-01, friday, tomorrow";
    due.setAttribute("aria-label", "Due date");
    due.addEventListener("change", () => {
      run(() => api("PATCH", `/api/node/${node.id}`, { due: due.value.trim() }));
    });
    row("Due", due);
  }

  row("Tags", tagEditor(node));
  return grid;
}

function selectControl(options, current, onChange) {
  const select = document.createElement("select");
  select.className = "field-control";

  for (const option of options) {
    const opt = document.createElement("option");
    opt.value = option;
    opt.textContent = option;
    opt.selected = option === current;
    select.append(opt);
  }
  select.addEventListener("change", () => run(() => onChange(select.value)));
  return select;
}

function tagEditor(node) {
  const wrap = document.createElement("div");
  wrap.className = "tag-editor";

  for (const tag of node.tags) {
    const chip = document.createElement("span");
    chip.className = "tag";
    chip.append(`#${tag}`);

    const remove = document.createElement("button");
    remove.textContent = "×";
    remove.setAttribute("aria-label", `Remove tag ${tag}`);
    remove.addEventListener("click", () =>
      run(() => api("PATCH", `/api/node/${node.id}`, { removeTag: tag })));

    chip.append(remove);
    wrap.append(chip);
  }

  const input = document.createElement("input");
  input.placeholder = "+ tag";
  input.setAttribute("aria-label", "Add a tag");
  input.addEventListener("keydown", (event) => {
    if (event.key !== "Enter") return;
    event.preventDefault();
    const tag = input.value.trim();
    if (tag) run(() => api("PATCH", `/api/node/${node.id}`, { addTag: tag }));
  });
  wrap.append(input);

  return wrap;
}

function bodyFor(node) {
  const frag = document.createDocumentFragment();

  const body = document.createElement("textarea");
  body.className = "detail__body";
  body.value = node.body || "";
  body.placeholder = "Write something. Markdown is stored as you type it.";
  body.spellcheck = true;
  body.setAttribute("aria-label", "Body");

  const save = () => {
    if (body.value === (node.body || "")) return;
    run(() => api("PATCH", `/api/node/${node.id}`, { body: body.value }));
  };

  body.addEventListener("blur", save);
  body.addEventListener("keydown", (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
      event.preventDefault();
      save();
      toast("Saved");
    }
    // Escape leaves the field rather than closing the pane, so a stray press
    // cannot discard what was typed.
    if (event.key === "Escape") { event.stopPropagation(); body.blur(); }
  });

  const hint = document.createElement("span");
  hint.className = "eyebrow detail__savehint";
  hint.textContent = "Saves when you click away, or press ⌘↵";

  frag.append(body, hint);
  return frag;
}

function section(label, content) {
  const wrap = document.createElement("div");
  wrap.className = "detail__section";

  const heading = document.createElement("span");
  heading.className = "eyebrow";
  heading.textContent = label;

  wrap.append(heading, content);
  return wrap;
}

function linkList(links, fromID) {
  const list = document.createElement("ul");
  list.className = "linklist";

  for (const link of links) {
    const li = document.createElement("li");

    const ref = document.createElement("span");
    ref.className = "ref";
    ref.textContent = link.ref;
    li.append(ref);

    if (link.pending) {
      // A link may point at something written on another machine that has not
      // synced yet. Saying so beats hiding it or showing a broken row.
      const pending = document.createElement("span");
      pending.className = "is-pending";
      pending.textContent = "not synced yet";
      li.append(pending);
    } else {
      const open = document.createElement("button");
      open.textContent = link.title;
      open.addEventListener("click", () => select(link.id));
      li.append(open);
    }

    if (fromID) {
      const remove = iconButton("Remove reference", "×", () =>
        run(() => api("POST", `/api/node/${fromID}/link`, { target: link.id, remove: true })));
      remove.style.marginLeft = "auto";
      li.append(remove);
    }

    list.append(li);
  }
  return list;
}

function historyList(history) {
  const list = document.createElement("ul");
  list.className = "history";

  for (const e of history) {
    const li = document.createElement("li");

    const ref = document.createElement("span");
    ref.className = "ref";
    ref.textContent = e.ref;

    const action = document.createElement("span");
    action.className = "history__action";
    action.textContent = e.action;

    const detail = document.createElement("span");
    detail.className = "history__detail";
    // Absent rather than empty when the event carried nothing beyond its
    // action, so it must not become the string "undefined".
    detail.textContent = e.detail || "";

    const at = document.createElement("span");
    at.className = "history__at";
    const when = new Date(e.at);
    at.textContent = when.toLocaleString(undefined, {
      month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
    });
    at.title = `${when.toLocaleString()} by ${e.by}`;

    const middle = document.createElement("span");
    middle.style.minWidth = "0";
    middle.append(action, " ", detail);

    li.append(ref, middle, at);
    list.append(li);
  }
  return list;
}

function footFor(node) {
  const foot = document.createElement("div");
  foot.className = "detail__foot";

  const link = document.createElement("button");
  link.className = "btn";
  link.textContent = "Link to…";
  link.addEventListener("click", async () => {
    const target = await ask("Handle or title to link to", { confirm: "Link" });
    if (target) run(() => api("POST", `/api/node/${node.id}/link`, { target }), "Linked");
  });

  const remove = document.createElement("button");
  remove.className = "btn btn--danger";
  remove.textContent = "Delete";
  remove.style.marginLeft = "auto";
  remove.addEventListener("click", () => deleteNode(node));

  foot.append(link, remove);
  return foot;
}

async function deleteNode(node) {
  const result = await run(() => api("DELETE", `/api/node/${node.id}`));
  if (!result) return;

  lastDeleted = result.id;
  closeDetail();

  // Deletion is an event, not an erasure, so undo is always available. The
  // offer is made here because the alternative is a confirmation dialog on
  // every delete, which is worse for something this reversible.
  toast(`Deleted “${result.title}”`, {
    action: { label: "Undo", run: () => restore(result.id) },
  });
}

function restore(id) {
  run(() => api("POST", `/api/node/${id}/restore`), "Restored");
}

function iconButton(label, glyph, onClick) {
  const button = document.createElement("button");
  button.className = "btn btn--quiet btn--icon";
  button.textContent = glyph;
  button.setAttribute("aria-label", label);
  button.title = label;
  button.addEventListener("click", onClick);
  return button;
}

function autoGrow(field) {
  field.style.height = "auto";
  field.style.height = `${field.scrollHeight}px`;
}

// shortPath keeps the last few segments of a location. The leading directories
// are almost never the part that identifies a project, and a temporary or
// deeply nested path would otherwise take four wrapped lines to say nothing.
function shortPath(path, keep = 3) {
  const parts = path.split("/").filter(Boolean);
  if (parts.length <= keep) return path;
  return "…/" + parts.slice(-keep).join("/");
}

function escapeHTML(s) {
  return s.replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

/* -------------------------------------------------------------- create */

async function create(kind) {
  const title = await ask(kind === "task" ? "New task" : "New note");
  if (!title) return;

  const created = await run(() =>
    api("POST", "/api/node", { kind, title, notebook: view.notebook }));

  // The cursor lands on what was just made, which is almost always what the
  // next action is aimed at.
  if (created) select(created.id);
}

/* -------------------------------------------------------------- events */

// The server pushes a version number whenever the project changes, including
// when the change came from the command line or from another machine's sync.
function watch() {
  const stream = new EventSource(`/api/events?token=${encodeURIComponent(TOKEN)}`);

  let seen = 0;
  stream.addEventListener("message", (event) => {
    const version = Number(event.data);
    if (version > seen) {
      // The first message only establishes the baseline; there is nothing new
      // to fetch at that point.
      if (seen !== 0) load();
      seen = version;
    }
  });

  // EventSource reconnects on its own, so an error only needs reporting if it
  // persists; a laptop waking up should not produce a warning.
  stream.addEventListener("error", () => {
    if (stream.readyState === EventSource.CLOSED) {
      setTimeout(watch, 3000);
    }
  });
}

/* ------------------------------------------------------------ keyboard */

// typing reports whether a keystroke belongs to a field rather than to the
// page, so single-key shortcuts never swallow what someone is writing.
function typing(event) {
  const t = event.target;
  return t instanceof HTMLInputElement
      || t instanceof HTMLTextAreaElement
      || t instanceof HTMLSelectElement
      || t.isContentEditable
      || el.ask.open;
}

function moveSelection(delta) {
  if (!view.entries.length) return;

  const at = view.entries.findIndex((n) => n.id === view.selected);
  const next = Math.max(0, Math.min(view.entries.length - 1, at < 0 ? 0 : at + delta));

  select(view.entries[next].id);
  document.querySelector(`.row[data-id="${view.entries[next].id}"]`)
    ?.scrollIntoView({ block: "nearest" });
}

document.addEventListener("keydown", (event) => {
  // Escape works everywhere: it steps back out of the search, then the detail.
  if (event.key === "Escape" && !el.ask.open) {
    if (document.activeElement === el.search) { el.search.blur(); return; }
    if (view.query) { el.search.value = ""; view.query = ""; load(); return; }
    if (view.selected) { closeDetail(); return; }
  }
  if (typing(event) || event.metaKey || event.ctrlKey || event.altKey) return;

  switch (event.key) {
    case "j": case "ArrowDown": event.preventDefault(); moveSelection(1); break;
    case "k": case "ArrowUp":   event.preventDefault(); moveSelection(-1); break;
    case "g": moveSelection(-view.entries.length); break;
    case "G": moveSelection(view.entries.length); break;

    case "/": event.preventDefault(); el.search.focus(); el.search.select(); break;
    case "n": event.preventDefault(); create("note"); break;
    case "t": event.preventDefault(); create("task"); break;

    case " ": {
      event.preventDefault();
      const n = view.entries.find((x) => x.id === view.selected);
      if (n?.kind === "task") {
        run(() => api("PATCH", `/api/node/${n.id}`, {
          status: n.status === "done" ? "open" : "done",
        }));
      }
      break;
    }
    case "u": if (lastDeleted) restore(lastDeleted); break;
  }
});

/* ---------------------------------------------------------------- wire */

let searchTimer;
el.search.addEventListener("input", () => {
  clearTimeout(searchTimer);
  // Long enough that a fast typist makes one request per word, short enough
  // that the list feels like it is keeping up.
  searchTimer = setTimeout(() => {
    view.query = el.search.value.trim();
    load();
  }, 140);
});

el.sort.addEventListener("change", () => {
  view.sort = el.sort.value;
  load();
});

el.filters.addEventListener("click", (event) => {
  const chip = event.target.closest(".chip");
  if (!chip) return;

  const { filter, value } = chip.dataset;
  if (view.filters[filter] === value) delete view.filters[filter];
  else view.filters[filter] = value;

  load();
});

$("new-note").addEventListener("click", () => create("note"));
$("new-task").addEventListener("click", () => create("task"));

$("new-notebook").addEventListener("click", async () => {
  const title = await ask("New notebook");
  if (title) run(() => api("POST", "/api/notebook", { title }), `Created ${title}`);
});

$("sync").addEventListener("click", async () => {
  const button = $("sync");
  button.disabled = true;
  button.textContent = "Syncing";

  try {
    const res = await api("POST", "/api/sync", { push: false });
    toast(res.committed ? `Committed on ${res.branch}` : "Nothing to commit");
  } catch (err) {
    toast(err.message, { error: true });
  } finally {
    button.disabled = false;
    button.textContent = "Sync";
  }
});

$("menu").addEventListener("click", () => {
  const open = el.rail.classList.toggle("is-open");
  $("menu").setAttribute("aria-expanded", String(open));
});

// Theme: the system preference is the default, and the toggle overrides it.
// The choice is remembered because it is a property of the reader, not of the
// project.
const THEME_KEY = "gnotes-theme";

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
  if (theme === "auto") localStorage.removeItem(THEME_KEY);
  else localStorage.setItem(THEME_KEY, theme);
}

applyTheme(localStorage.getItem(THEME_KEY) || "auto");

$("theme").addEventListener("click", () => {
  const order = ["auto", "light", "dark"];
  const next = order[(order.indexOf(document.documentElement.dataset.theme) + 1) % order.length];
  applyTheme(next);
  toast(`Theme: ${next}`);
});

/* ---------------------------------------------------------------- start */

if (!TOKEN) {
  el.empty.hidden = false;
  el.empty.textContent = "No access token. Open the address gnotes printed, including its ?token=… part.";
} else {
  load();
  watch();
}
