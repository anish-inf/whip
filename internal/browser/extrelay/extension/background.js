// loopy browser extension service worker.
//
// Clicking the toolbar icon on a tab pins it: the worker attaches
// chrome.debugger to that tab, opens a WebSocket to the loopy relay
// (address + token from relay.json, which `loopy browser install` writes
// next to this file), and pipes raw CDP both ways. loopy's rod backend talks
// CDP to the relay; this worker forwards it to the tab and streams events
// back. Clicking the icon again (or a debugger detach) unpins.
//
// Only the one pinned tab is ever drivable, and only while pinned.

let ws = null;
let pinnedTabId = null;
// rod addresses the synthesized single session "loopy-ext"; chrome.debugger
// is inherently single-session, so we strip that field both ways.
const SESSION_ID = "loopy-ext";

async function relayEndpoint() {
  const url = chrome.runtime.getURL("relay.json");
  const res = await fetch(url);
  if (!res.ok) throw new Error("relay.json missing — run `loopy browser install`");
  const { addr, token, autoAttach } = await res.json();
  if (!addr || !token) throw new Error("relay.json is incomplete — re-run `loopy browser install`");
  return {
    ws: `ws://${addr}/ext?token=${encodeURIComponent(token)}`,
    log: `http://${addr}/swlog?token=${encodeURIComponent(token)}`,
    autoAttach: !!autoAttach,
  };
}

// swlog POSTs a diagnostic line to the relay (test/debug): the SW's console
// is hard to capture because it runs and suspends before a debugger attaches,
// so each step of pin/autoAttach reports over plain HTTP instead. Reads the
// relay address directly from relay.json so it works even when
// relayEndpoint() itself is what's failing.
async function swlog(step, extra) {
  try {
    const res = await fetch(chrome.runtime.getURL("relay.json"));
    if (!res.ok) { return; }
    const { addr, token } = await res.json();
    if (!addr) { return; }
    await fetch(`http://${addr}/swlog?token=${encodeURIComponent(token || "")}`, {
      method: "POST",
      body: step + (extra !== undefined ? ": " + extra : ""),
    });
  } catch (_) {}
}

// autoAttach (test/CI only): when relay.json sets it, pin the currently
// active tab on startup without waiting for an icon click — lets the E2E
// test load the extension and drive a tab unattended. Off by default; the
// production flow is always an explicit icon click. Retries: the service
// worker can start before Chrome has opened its first tab.
async function maybeAutoAttach() {
  let auto = false;
  try {
    auto = (await relayEndpoint()).autoAttach;
  } catch (err) {
    swlog("relay-read-failed", String(err));
    return;
  }
  swlog("autoAttach-flag", String(auto));
  if (!auto) return;
  for (let i = 0; i < 30 && pinnedTabId == null; i++) {
    try {
      const tabs = await chrome.tabs.query({});
      const tab = tabs.find((t) => t.url && !t.url.startsWith("chrome://")) || tabs[0];
      swlog("poll", `i=${i} tabs=${tabs.length} tab=${tab && tab.id} url=${tab && tab.url}`);
      if (tab && tab.id != null) {
        await pin(tab.id);
        swlog("pinned", `tabId=${tab.id}`);
        return;
      }
    } catch (err) {
      swlog("autoAttach-attempt-failed", String(err && err.message || err));
    }
    await new Promise((r) => setTimeout(r, 500));
  }
}

function setBadge(on) {
  const text = on ? "●" : "";
  const color = on ? "#16a34a" : "#000000";
  if (pinnedTabId != null) {
    chrome.action.setBadgeText({ text, tabId: pinnedTabId }).catch(() => {});
    if (on) chrome.action.setBadgeBackgroundColor({ color, tabId: pinnedTabId }).catch(() => {});
  }
}

async function pin(tabId) {
  // Attach the debugger first so failures surface before we open the socket.
  await swlog("pin-enter", `tabId=${tabId}`);
  await chrome.debugger.attach({ tabId }, "1.3");
  await swlog("debugger-attached", `tabId=${tabId}`);
  pinnedTabId = tabId;
  const { ws: endpoint } = await relayEndpoint();
  ws = new WebSocket(endpoint);
  ws.onopen = async () => {
    await swlog("ws-open", `tabId=${tabId}`);
    // Tell the relay which tab it is driving so Target.getTargets can
    // describe it accurately.
    let title = "", url = "";
    try {
      const t = await chrome.tabs.get(tabId);
      title = t.title || ""; url = t.url || "";
    } catch (_) {}
    ws.send(JSON.stringify({ method: "loopy.attached", params: { tabId, title, url } }));
    setBadge(true);
  };
  ws.onerror = () => { swlog("ws-error", `tabId=${tabId}`); };
  ws.onmessage = (ev) => {
    // CDP request from loopy → the pinned tab.
    try {
      const msg = JSON.parse(ev.data);
      if (msg.sessionId === SESSION_ID) delete msg.sessionId;
      chrome.debugger
        .sendCommand({ tabId }, msg.method, msg.params || {})
        .then((result) => {
          if (msg.id) send({ id: msg.id, result: result || {} });
        })
        .catch((err) => {
          if (msg.id) send({ id: msg.id, error: { code: -32000, message: String(err && err.message || err) } });
        });
    } catch (_) {}
  };
  ws.onclose = () => unpin();
  ws.onerror = () => {};
}

function send(obj) {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(obj));
}

async function unpin() {
  const tabId = pinnedTabId;
  pinnedTabId = null;
  if (ws) {
    const s = ws;
    ws = null;
    try { s.close(); } catch (_) {}
  }
  if (tabId != null) {
    setBadge(false);
    try { await chrome.debugger.detach({ tabId }); } catch (_) {}
  }
}

// Tab events → loopy (as CDP events). chrome.debugger.onEvent fires for the
// pinned tab while attached.
chrome.debugger.onEvent.addListener((source, method, params) => {
  if (source.tabId === pinnedTabId) send({ method, params: params || {} });
});

// Chrome detached the debugger (user clicked "Cancel" on the infobar, etc.).
chrome.debugger.onDetach.addListener((source) => {
  if (source.tabId === pinnedTabId) unpin();
});

// Toolbar icon toggles the pin on the active tab.
chrome.action.onClicked.addListener(async (tab) => {
  if (pinnedTabId === tab.id) {
    await unpin();
    return;
  }
  if (pinnedTabId != null) await unpin(); // re-pinning moves the pin
  try {
    await pin(tab.id);
  } catch (err) {
    console.error("loopy: pin failed:", err);
    pinnedTabId = null;
  }
});

// Keep the service worker alive while pinned (MV3 suspends idle workers).
setInterval(() => {
  if (pinnedTabId != null && ws && ws.readyState === WebSocket.OPEN) {
    send({ method: "loopy.ping", params: {} });
  }
}, 20000);

// Wake the service worker for the lifecycle events that matter: install,
// browser startup, and (test/CI) the autoAttach poll. MV3 SWs are dormant
// until an event fires — top-level code alone won't reliably run autoAttach,
// so it's driven by these listeners.
chrome.runtime.onInstalled.addListener(() => { swlog("onInstalled"); maybeAutoAttach(); });
chrome.runtime.onStartup.addListener(() => { swlog("onStartup"); maybeAutoAttach(); });

// Test/CI hook: pin the active tab without a click when relay.json sets
// autoAttach. No-op in the production flow (flag absent). Also run once at
// top level in case the worker is already awake.
swlog("sw-start", "top-level");
maybeAutoAttach();
