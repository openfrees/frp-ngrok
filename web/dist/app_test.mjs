import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

class FakeNode {
  constructor(tagName) {
    this.tagName = tagName;
    this.children = [];
    this.className = "";
    this.dataset = {};
    this.style = {};
    this.hidden = false;
    this.disabled = false;
    this.textContent = "";
  }

  appendChild(child) {
    this.children.push(child);
    return child;
  }

  removeChild(child) {
    this.children.splice(this.children.indexOf(child), 1);
    return child;
  }

  get firstChild() {
    return this.children[0] || null;
  }
}

function response(body) {
  return Promise.resolve({
    ok: true,
    status: 200,
    json: function () { return Promise.resolve(body); }
  });
}

function nodeText(node) {
  return [node.textContent].concat(node.children.map(nodeText)).join(" ");
}

test("未设置快捷键时用短标签显示为面板", function () {
  const source = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  assert.doesNotMatch(source, /仅命令面板/);
  assert.match(source, /if \(!combo\) return t\("面板"\);/);
});

test("快捷键设置列表支持两行截断换行滚动", function () {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const css = readFileSync(new URL("./style.css", import.meta.url), "utf8");

  assert.match(app, /el\("div", "card hotkey-list"\)/);
  assert.match(app, /el\("div", "row hotkey-row"\)/);
  assert.match(app, /el\("div", "row-main hotkey-main"\)/);
  assert.match(app, /el\("div", "row-sub hotkey-meta"/);
  assert.match(app, /el\("div", "row-actions hotkey-actions"\)/);

  assert.match(css, /\.modal\s*\{[^}]*display: flex;/s);
  assert.match(css, /\.modal\s*\{[^}]*flex-direction: column;/s);
  assert.match(css, /\.modal\s*\{[^}]*overflow: hidden;/s);
  assert.match(css, /\.modal-body\s*\{[^}]*overflow-y: auto;[^}]*min-height: 0;/s);
  assert.match(css, /\.hotkey-meta\s*\{[^}]*-webkit-line-clamp: 2;[^}]*overflow-wrap: anywhere;[^}]*word-break: break-word;/s);
  assert.match(css, /\.hotkey-actions\s*\{[^}]*margin-left: 16px;/s);
});

test("快捷键标题悬浮时不按链接样式变色加下划线", function () {
  const css = readFileSync(new URL("./style.css", import.meta.url), "utf8");
  assert.match(css, /\.hotkey-main \.row-url:hover\s*\{[^}]*color: var\(--text\);[^}]*text-decoration: none;/s);
});

test("快捷键设置列表顶部展示面板触发键并支持拖拽保存顺序", function () {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const css = readFileSync(new URL("./style.css", import.meta.url), "utf8");

  assert.match(app, /subtitle: comboDisplay\(currentPaletteCombo\(\)\) \+ t\(" · 按下后在屏幕中央搜索并执行快捷命令；总开关关闭时不生效。"\)/);
  assert.doesNotMatch(app, /paletteRow/);
  assert.match(app, /function hotkeyDisplayItems\(\)/);
  assert.match(app, /function saveHotkeysOrder\(ordered\)/);
  assert.match(app, /var items = hotkeyDisplayItems\(\);/);
  assert.match(app, /items\.forEach/);
  assert.match(app, /row\.draggable = true;/);
  assert.match(app, /row\.dataset\.id = it\.id;/);
  assert.match(app, /row\.ondragstart = function/);
  assert.match(app, /row\.ondragover = function/);
  assert.match(app, /row\.ondrop = function/);
  assert.match(app, /saveHotkeysOrder\(ordered\)/);
  assert.match(app, /items\.unshift\(item\);/);
  assert.match(app, /orderVersion: 1/);

  assert.match(css, /\.hotkey-row\s*\{[^}]*cursor: grab;/s);
  assert.match(css, /\.hotkey-row\.is-dragging\s*\{[^}]*opacity: 0\.45;/s);
});

test("快捷键插件状态按钮用当前状态文案和颜色", function () {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const css = readFileSync(new URL("./style.css", import.meta.url), "utf8");

  assert.match(app, /"btn plugin-status-toggle " \+ \(s\.enabled \? "btn-primary" : "btn-danger-dashed"\)/);
  assert.match(app, /s\.enabled \? t\("开启中"\) : t\("关闭中"\)/);
  assert.doesNotMatch(app, /row\.appendChild\(el\("span", "port-chip", s\.enabled \? "已开启" : "未开启"\)\)/);
  assert.match(css, /\.btn-danger-dashed\s*\{[^}]*background: var\(--surface\);[^}]*border-style: dashed;[^}]*color: var\(--danger\);/s);
  assert.match(css, /\.plugin-status-toggle\s*\{[^}]*min-width: 74px;/s);
});

test("插件页设置按钮排在状态开关前面", function () {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const hotkey = app.match(/function hotkeyPluginRow\(\) \{[\s\S]*?\n  \}\n/)[0];
  const accessLog = app.match(/function accessLogPluginRow\(\) \{[\s\S]*?\n  \}\n/)[0];
  const portSites = app.match(/function portSitesPluginRow\(\) \{[\s\S]*?\n  \}\n/)[0];
  assert.match(hotkey, /acts\.appendChild\(setBtn\);\s*acts\.appendChild\(toggle\);/);
  assert.match(accessLog, /acts\.appendChild\(setBtn\);\s*acts\.appendChild\(toggle\);/);
  assert.match(portSites, /el\("button", "btn", t\("去管理"\)\)/);
  assert.match(portSites, /goBtn\.onclick = function \(\) \{ showView\("portsites"\); \}/);
  assert.match(portSites, /acts\.appendChild\(goBtn\);\s*acts\.appendChild\(toggle\);/);
});

test("打开终端说明匹配窗口和标签页行为", function () {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");

  assert.match(app, /terminal: "打开系统自带「终端」；没有窗口时新建窗口，已有窗口时新建标签页并执行这条命令。"/);
  assert.doesNotMatch(app, /terminal: "打开系统自带「终端」，新建一个窗口/);
});

test("旧后台返回代理状态时仍按可达点绿且不展示代理提示", async function () {
  const nodes = new Map();
  const document = {
    body: new FakeNode("body"),
    hidden: false,
    createElement: function (tag) { return new FakeNode(tag); },
    getElementById: function (id) {
      if (!nodes.has(id)) nodes.set(id, new FakeNode("div"));
      return nodes.get(id);
    },
    addEventListener: function () {},
    querySelectorAll: function () { return []; }
  };

  const context = vm.createContext({
    URLSearchParams,
    document,
    history: { replaceState: function () {} },
    localStorage: { getItem: function () { return ""; }, setItem: function () {} },
    location: { pathname: "/", search: "" },
    navigator: {},
    Promise,
    setInterval,
    setTimeout,
    window: { isSecureContext: false },
    fetch: function (path) {
      if (path === "/api/state") return new Promise(function () {});
      if (path === "/api/check/server") {
        return response({
          tcp: { result: "hijacked" },
          loginState: "running",
          dns: { result: "hijacked", host: "probe.example.com", ips: ["198.18.0.1"] },
          advice: "服务器验收通过"
        });
      }
      if (path === "/api/check/tunnels") return response({ results: [] });
      throw new Error("unexpected request: " + path);
    }
  });

  const source = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  vm.runInContext(source, context);

  nodes.get("btnRunCheck").onclick();
  await new Promise(function (resolve) { setTimeout(resolve, 0); });

  const result = nodes.get("checkResult");
  const firstStep = result.children[0].children[0];
  const dnsStep = result.children[0].children[2];
  assert.equal(firstStep.children[0].className, "dot ok");
  assert.equal(dnsStep.children[0].className, "dot ok");
  assert.doesNotMatch(nodeText(result), /代理|VPN|握手结果不可信|测不准|已跳过/);
  assert.equal(result.children[1].className, "verdict ok");
});

test("首次进入空白的连通检测页时自动检测且不重复触发", function () {
  const nodes = new Map();
  const document = {
    body: new FakeNode("body"),
    hidden: false,
    createElement: function (tag) { return new FakeNode(tag); },
    getElementById: function (id) {
      if (!nodes.has(id)) nodes.set(id, new FakeNode("div"));
      return nodes.get(id);
    },
    addEventListener: function () {},
    querySelectorAll: function () { return []; }
  };
  let serverChecks = 0;
  const window = { isSecureContext: false };
  const context = vm.createContext({
    __testState: {
      current: { name: "test", domain: "example.com", domainMode: "wildcard" },
      client: { state: "running" },
      profiles: [],
      tunnels: []
    },
    URLSearchParams,
    clearInterval,
    document,
    history: { replaceState: function () {} },
    localStorage: { getItem: function () { return ""; }, setItem: function () {} },
    location: { pathname: "/", search: "" },
    navigator: {},
    Promise,
    setInterval,
    setTimeout,
    window,
    fetch: function (path) {
      if (path === "/api/check/server") {
        serverChecks++;
        return new Promise(function () {});
      }
      throw new Error("unexpected request: " + path);
    }
  });

  const source = readFileSync(new URL("./app.js", import.meta.url), "utf8")
    .replace("  boot();\n})();", "  state = __testState;\n  window.showViewForTest = showView;\n})();");
  vm.runInContext(source, context);

  window.showViewForTest("check");
  window.showViewForTest("check");

  assert.equal(serverChecks, 1);
  assert.equal(nodes.get("btnRunCheck").disabled, true);
  assert.match(nodes.get("checkResult").firstChild.textContent, /正在检测/);
});

test("切换中文后连通检测卡片用缓存结果重绘，不再发请求", async function () {
  const nodes = new Map();
  const document = {
    body: new FakeNode("body"),
    documentElement: { lang: "en" },
    hidden: false,
    createElement: function (tag) { return new FakeNode(tag); },
    getElementById: function (id) {
      if (!nodes.has(id)) nodes.set(id, new FakeNode("div"));
      return nodes.get(id);
    },
    addEventListener: function () {},
    querySelectorAll: function () { return []; }
  };
  let serverChecks = 0;
  const windowObj = { isSecureContext: false };
  const context = vm.createContext({
    __testState: {
      current: { name: "test", domain: "example.com", domainMode: "wildcard" },
      client: { state: "running" },
      profiles: [],
      tunnels: []
    },
    URLSearchParams,
    clearInterval,
    document,
    history: { replaceState: function () {} },
    localStorage: { getItem: function () { return ""; }, setItem: function () {} },
    location: { pathname: "/", search: "" },
    navigator: {},
    Promise,
    setInterval,
    setTimeout,
    window: windowObj,
    fetch: function (path) {
      if (path === "/api/check/server") {
        serverChecks++;
        return response({
          tcp: { result: "reachable" },
          loginState: "running",
          dns: { result: "ok", host: "probe.example.com", ips: ["198.18.0.1"] },
          advice: "服务器验收通过，可以开始加隧道了。"
        });
      }
      if (path === "/api/check/tunnels") return response({ results: [] });
      throw new Error("unexpected request: " + path);
    }
  });

  vm.runInContext(readFileSync(new URL("./i18n.js", import.meta.url), "utf8"), context);
  vm.runInContext(
    readFileSync(new URL("./app.js", import.meta.url), "utf8")
      .replace("  boot();\n})();", "  state = __testState;\n  window.showViewForTest = showView;\n})();"),
    context
  );

  windowObj.I18N.setLocale("en");
  nodes.get("btnRunCheck").onclick();
  await new Promise(function (resolve) { setTimeout(resolve, 0); });

  assert.match(nodeText(nodes.get("checkResult")), /Server port reachable/);
  assert.match(nodeText(nodes.get("checkResult")), /Server checks out/);

  windowObj.I18N.setLocale("zh-CN");
  windowObj.showViewForTest("check");

  assert.equal(serverChecks, 1);
  assert.match(nodeText(nodes.get("checkResult")), /① 服务器端口可达/);
  assert.match(nodeText(nodes.get("checkResult")), /服务器验收通过，可以开始加隧道了。/);
  assert.doesNotMatch(nodeText(nodes.get("checkResult")), /Server port reachable/);
});

test("访问日志插件开启后才露出隧道日志入口", function () {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
  const css = readFileSync(new URL("./style.css", import.meta.url), "utf8");

  assert.match(app, /\{ id: "access-log", name: "访问日志", sub: "记录隧道访问来源 IP、路径、状态码与时间" \}/);
  assert.doesNotMatch(app, /id: "access-log"[^}]*规划中/);
  assert.match(app, /if \(state\.accessLog\) \{/);
  assert.match(app, /el\("button", "btn btn-sm", t\("日志"\)\)/);
  assert.match(app, /function openTunnelLog\(tn\)/);
  assert.match(app, /logKind = "tunnel"/);
  assert.match(app, /btn\.dataset\.logKind/);
  assert.match(app, /if \(!pluginOn\) logKind = "client"/);
  assert.match(app, /nav\.hidden = !\(pluginOn && logKind === "tunnel"\)/);
  assert.match(app, /\/api\/plugins\/access-log\/tunnels\/" \+ selectedLogPort \+ "\/log/);
  assert.match(app, /tn\.logging \? t\("记录中"\) : t\("已暂停"\)/);
  assert.match(app, /t\("本机 :"\) \+ tn\.localPort \+ t\("  ·  日志 "\) \+ tn\.sizeText/);

  assert.match(html, /id="logKinds"/);
  assert.match(html, /data-log-kind="client"/);
  assert.match(html, /data-log-kind="tunnel"/);
  assert.match(html, /id="logTunnelNav"/);

  assert.match(css, /\.log-kinds\s*\{/);
  assert.match(css, /\.log-tunnel-nav\s*\{[^}]*width: 220px;/s);
  assert.match(css, /\.log-tunnel-item\.is-active\s*\{[^}]*background: var\(--signal-soft\);/s);
});

test("访问日志插件状态按钮沿用开启中关闭中样式", function () {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  assert.match(app, /function accessLogPluginRow\(\)/);
  assert.match(app, /put\("\/api\/plugins\/access-log", \{ enabled: want \}\)/);
  assert.match(app, /s\.enabled \? t\("开启中"\) : t\("关闭中"\)/);
});

test("本地端口管理替换空白端口占位并露出独立 Tab", function () {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
  const css = readFileSync(new URL("./style.css", import.meta.url), "utf8");

  assert.doesNotMatch(app, /blank-site/);
  assert.doesNotMatch(app, /开启空白端口网站/);
  assert.match(app, /\{ id: "port-sites", name: "本地端口管理", sub: "可以管理本地端口甚至通过隧道程序单独启动网站端口服务" \}/);
  assert.match(app, /function portSitesPluginRow\(\)/);
  assert.match(app, /put\("\/api\/plugins\/port-sites", \{ enabled: want \}\)/);
  assert.match(app, /s\.enabled \? t\("开启中"\) : t\("关闭中"\)/);
  assert.match(app, /\["empty", "tunnels", "check", "deploy", "logs", "settings", "plugins", "portsites"\]/);
  assert.match(app, /if \(name === "portsites" && !\(state && state\.portSites\)\) name = "plugins"/);
  assert.match(app, /function renderPortSites\(\)/);
  assert.match(app, /function confirmDeletePortSite\(site\)/);
  assert.match(app, /delFiles\.checked = !!site\.deleteFilesDefault/);
  assert.match(app, /drop\.ondrop = function/);
  assert.match(app, /fd\.append\("file", file, file\.name\)/);
  assert.match(app, /\/api\/plugins\/port-sites\/pick-dir/);
  assert.match(app, /el\("button", "btn", t\("选择"\)\)/);
  assert.match(app, /data && data\.canceled/);
  assert.match(app, /el\("div", "row portsite-file-row" \+ \(f\.isDir \? " is-dir" : ""\)\)/);
  assert.match(app, /el\("button", "btn", t\("打开文件夹"\)\)/);
  assert.match(app, /encodeURIComponent\(confirming\.name\)/);
  assert.match(app, /confirming = f;/);
  assert.match(app, /el\("button", "btn btn-danger", t\("确认删除"\)\)/);
  assert.doesNotMatch(app, /window\.confirm\("删除 " \+ f\.name/);
  assert.match(app, /opt\.leftText/);
  assert.match(app, /function paintCrumbs\(\)/);
  assert.match(app, /el\("button", "portsite-crumb", t\("站点根"\)\)/);
  assert.match(app, /enterDir\(joinPortSiteDir\(dir, f\.name\)\)/);
  assert.match(app, /el\("button", "btn btn-sm", t\("上一页"\)\)/);
  assert.match(app, /el\("button", "btn btn-sm", t\("下一页"\)\)/);
  assert.match(app, /offset=" \+ offset \+ "&limit=" \+ limit/);

  assert.match(html, /data-nav="portsites"/);
  assert.match(html, /id="tabPortSites"/);
  assert.match(html, /id="view-portsites"/);
  assert.match(html, /id="btnAddPortSite"/);
  const pluginsIdx = html.indexOf('data-nav="plugins"');
  const portIdx = html.indexOf('data-nav="portsites"');
  assert.ok(pluginsIdx > 0 && portIdx > pluginsIdx, "端口管理 Tab 应在插件右边");

  assert.match(css, /\.portsite-card\s*\{/);
  assert.match(css, /\.dropzone\.is-over\s*\{/);
  assert.match(css, /\.port-chip\s*\{[^}]*display: inline-flex;[^}]*align-items: center;[^}]*line-height: 1;/s);
  assert.match(css, /\.portsite-file-row\s*\{/);
  assert.match(css, /\.portsite-file-list\s*\{[^}]*overflow: auto;/s);
  assert.match(css, /\.portsite-crumbs\s*\{/);
  assert.match(css, /\.portsite-file-pager\s*\{/);
  assert.match(css, /\.field-inline\s*\{/);
});

test("端口站点卡片链接只负责跳转空白处和文件管理打开弹窗", function () {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const css = readFileSync(new URL("./style.css", import.meta.url), "utf8");
  const render = app.match(/function renderPortSites\(\) \{[\s\S]*?\n  \}\n/)[0];

  assert.match(render, /a\.onclick = function \(e\) \{ e\.stopPropagation\(\); \}/);
  assert.match(render, /card\.onclick = function \(\) \{ openPortSiteFiles\(site\); \}/);
  assert.match(render, /el\("button", "btn btn-sm", t\("文件管理"\)\)/);
  assert.match(render, /filesBtn\.onclick = function \(e\) \{\s*e\.stopPropagation\(\);\s*openPortSiteFiles\(site\);/);
  assert.match(render, /acts\.appendChild\(filesBtn\);\s*acts\.appendChild\(runBtn\);\s*acts\.appendChild\(delBtn\);/);
  assert.match(css, /\.portsite-card \.row-url\s*\{[^}]*display: inline-block;[^}]*max-width: 100%;/s);
});

test("插件页只保留三个已实现插件不再展示规划中占位", function () {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const list = app.match(/var PLUGINS = \[[\s\S]*?\];/)[0];
  assert.match(list, /id: "hotkeys"/);
  assert.match(list, /id: "port-sites"/);
  assert.match(list, /id: "access-log"/);
  assert.equal((list.match(/id: "/g) || []).length, 3);
  assert.doesNotMatch(list, /规划中/);
  assert.doesNotMatch(app, /function placeholderPluginRow/);
  assert.doesNotMatch(app, /证书到期提醒|流量统计|端口健康监控|Webhook 通知|配置自动备份/);
});

test("顶栏常驻中英文切换，设置页仍保留语言选项", function () {
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const css = readFileSync(new URL("./style.css", import.meta.url), "utf8");

  assert.match(html, /id="langSwitch"/);
  assert.match(html, /data-locale="en"/);
  assert.match(html, /data-locale="zh-CN"/);
  assert.ok(
    html.indexOf('id="langSwitch"') < html.indexOf('data-nav="settings"'),
    "语言开关应在顶栏设置按钮左边，打开任意页都能看到"
  );

  assert.match(app, /function applyLocale\(/);
  assert.match(app, /function paintLangSwitch\(/);
  assert.match(app, /mLang\.appendChild\(el\("h3", null, "Language \/ 语言"\)\)/);
  assert.match(css, /\.lang-switch\s*\{/);
});

test("部署页 DNS 表用单元格文案而不是把 t 函数源码填进去", function () {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const dnsStep = app.match(/function dnsStep\(d\) \{[\s\S]*?\n  \}\n/)[0];
  assert.match(dnsStep, /el\("th", null, tn\)/);
  assert.match(dnsStep, /el\("td", null, tn\)/);
  assert.doesNotMatch(dnsStep, /el\("th", null, t\)/);
  assert.doesNotMatch(dnsStep, /el\("td", null, t\)/);
});

test("按钮与语言开关用 flex + 行高 1 让文字垂直居中", function () {
  const css = readFileSync(new URL("./style.css", import.meta.url), "utf8");
  assert.match(css, /\.btn\s*\{[^}]*display: inline-flex;[^}]*align-items: center;[^}]*line-height: 1;/s);
  assert.match(css, /\.seg button\s*\{[^}]*display: inline-flex;[^}]*align-items: center;[^}]*justify-content: center;[^}]*line-height: 1;/s);
  assert.match(css, /\.seg\.lang-switch button\s*\{[^}]*display: inline-flex;[^}]*align-items: center;[^}]*justify-content: center;[^}]*line-height: 1;/s);
  assert.doesNotMatch(css, /\.lang-switch button\s*\{[^}]*line-height: 28px;/s);
});

test("轮询到菜单栏改过的语言后网页跟着切换", function () {
  const nodes = new Map();
  const settingsBtn = new FakeNode("button");
  settingsBtn.getAttribute = function (name) { return name === "data-i18n" ? "设置" : ""; };
  settingsBtn.textContent = "Settings";

  const document = {
    body: new FakeNode("body"),
    documentElement: { lang: "en" },
    hidden: false,
    createElement: function (tag) { return new FakeNode(tag); },
    createTextNode: function (text) {
      var n = new FakeNode("#text");
      n.textContent = text;
      return n;
    },
    getElementById: function (id) {
      if (!nodes.has(id)) nodes.set(id, new FakeNode("div"));
      return nodes.get(id);
    },
    addEventListener: function () {},
    querySelectorAll: function (sel) {
      if (sel === "[data-i18n]") return [settingsBtn];
      return [];
    }
  };
  const windowObj = { isSecureContext: false, document: document };
  const context = vm.createContext({
    __testState: {
      current: { name: "test", domain: "example.com", domainMode: "wildcard" },
      client: { state: "running" },
      profiles: [],
      tunnels: [],
      locale: "en"
    },
    URLSearchParams,
    clearInterval,
    document,
    history: { replaceState: function () {} },
    localStorage: { getItem: function () { return ""; }, setItem: function () {} },
    location: { pathname: "/", search: "" },
    navigator: {},
    Promise,
    setInterval,
    setTimeout,
    window: windowObj,
    fetch: function (path) {
      throw new Error("unexpected request: " + path);
    }
  });

  vm.runInContext(readFileSync(new URL("./i18n.js", import.meta.url), "utf8"), context);
  vm.runInContext(
    readFileSync(new URL("./app.js", import.meta.url), "utf8")
      .replace("  boot();\n})();", "  state = __testState;\n  window.onPolledStateForTest = onPolledState;\n})();"),
    context
  );

  windowObj.I18N.setLocale("en");
  windowObj.I18N.applyStatic();
  assert.equal(settingsBtn.textContent, "Settings");

  windowObj.onPolledStateForTest({
    current: { name: "test", domain: "example.com", domainMode: "wildcard" },
    client: { state: "running" },
    profiles: [],
    tunnels: [],
    locale: "zh-CN"
  });

  assert.equal(windowObj.I18N.locale, "zh-CN");
  assert.equal(settingsBtn.textContent, "设置");
});
