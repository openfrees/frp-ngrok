/* frp-ngrok 前端 · 无构建步骤，直接由后端内嵌分发 */
(function () {
  "use strict";

  var TOKEN_KEY = "frp-ngrok.token";

  function t(zh) {
    return (window.I18N && typeof window.I18N.t === "function") ? window.I18N.t(zh) : zh;
  }

  function currentLocale() {
    var loc = (window.I18N && window.I18N.locale) || "en";
    if (loc === "zh") loc = "zh-CN";
    return loc;
  }

  function applyLocale(id) {
    return put("/api/prefs", { locale: id }).then(function () {
      if (window.I18N) {
        window.I18N.setLocale(id);
        window.I18N.applyStatic();
      }
      return refresh();
    });
  }

  function paintLangSwitch() {
    var loc = currentLocale();
    Array.prototype.forEach.call(document.querySelectorAll("[data-locale]"), function (b) {
      b.classList.toggle("is-active", b.getAttribute("data-locale") === loc);
    });
  }

  function applyStateLocale(s) {
    var next = (s && s.locale) || "en";
    var changed = currentLocale() !== (next === "zh" || next === "zh-CN" ? "zh-CN" : "en");
    if (window.I18N) {
      window.I18N.setLocale(next);
      window.I18N.applyStatic();
    }
    paintLangSwitch();
    return changed;
  }

  function onPolledState(s) {
    var locChanged = applyStateLocale(s);
    var before = state && state.client ? state.client.state : "";
    var hadCurrent = !!(state && state.current);
    state = s;
    renderTop();
    syncPortSitesTab();
    if (locChanged) {
      showView(state.current ? currentView : "empty");
      return;
    }
    if (hadCurrent !== !!s.current) {
      showView(s.current ? "tunnels" : "empty");
    } else if (currentView === "tunnels" && before !== s.client.state) {
      renderTunnels();
    }
  }

  var state = null;
  var currentView = "tunnels";
  var logKind = "client";
  var selectedLogPort = 0;
  var logTimer = null;
  var pollTimer = null;
  var lastServerCheck = null;
  var lastTunnelChecks = null;

  /* ---------------- 令牌 ---------------- */

  function readToken() {
    var q = new URLSearchParams(location.search).get("token");
    if (q) {
      try { localStorage.setItem(TOKEN_KEY, q); } catch (e) { /* 隐私模式下不可用 */ }
      // 令牌不该留在地址栏里，免得被书签或历史记录带走
      history.replaceState(null, "", location.pathname);
      return q;
    }
    try { return localStorage.getItem(TOKEN_KEY) || ""; } catch (e) { return ""; }
  }

  var token = readToken();

  /* ---------------- 请求 ---------------- */

  function api(method, path, body) {
    var opts = {
      method: method,
      headers: { Authorization: "Bearer " + token }
    };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    return fetch(path, opts).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        if (!res.ok) {
          throw new Error((data.error ? t(data.error) : (t("请求失败") + " (HTTP " + res.status + ")")));
        }
        return data;
      });
    });
  }

  var get = function (p) { return api("GET", p); };
  var post = function (p, b) { return api("POST", p, b === undefined ? {} : b); };
  var put = function (p, b) { return api("PUT", p, b === undefined ? {} : b); };
  var del = function (p) { return api("DELETE", p); };

  /* ---------------- 小工具 ---------------- */

  function $(id) { return document.getElementById(id); }

  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  }

  function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); }

  function toast(msg, kind) {
    var root = $("toastRoot");
    var toastEl = el("div", "toast" + (kind ? " " + kind : ""), msg);
    root.appendChild(toastEl);
    setTimeout(function () {
      toastEl.style.transition = "opacity .3s";
      toastEl.style.opacity = "0";
      setTimeout(function () { toastEl.remove(); }, 300);
    }, kind === "bad" ? 4200 : 2600);
  }

  function fail(err) { toast(t(err.message || String(err)), "bad"); }

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    // http://127.0.0.1 在部分浏览器里不算安全上下文，退回旧接口
    return new Promise(function (resolve, reject) {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      try {
        document.execCommand("copy") ? resolve() : reject(new Error(t("复制失败")));
      } catch (e) {
        reject(e);
      } finally {
        ta.remove();
      }
    });
  }

  /* ---------------- 状态语义 ---------------- */

  var CLIENT_STATE = {
    running:      { dot: "ok",   label: "隧道运行中" },
    starting:     { dot: "warn", label: "正在连接" },
    login_failed: { dot: "bad",  label: "登录被拒绝" },
    unreachable:  { dot: "bad",  label: "连不上服务器" },
    crashed:      { dot: "bad",  label: "客户端异常" },
    stopped:      { dot: "off",  label: "已停止" }
  };

  function clientMeta(client) {
    var m = CLIENT_STATE[client && client.state] || CLIENT_STATE.stopped;
    return { dot: m.dot, label: t(m.label) };
  }

  /* ---------------- 域名模式 ----------------
     这里说的只是档案域名（底座）怎么用：
     泛域名：一张通配符证书带任意多个三级域名，挂靠的隧道各占一个名字。
     单域名：只有一个固定域名对外，挂靠它的隧道只能有一条。
     无底座：这台服务器不占任何档案域名，隧道只能各绑各的独立域名。
     三种情形下，隧道都还可以各自单绑一个独立域名，不受这个模式约束。
     后端字段缺失时按泛域名处理，与加模式之前的老档案保持一致。 */

  var NO_BASE = "未设底座";

  // hasBase 判断这台服务器还有没有底座。底座能被删掉，删掉之后 domainMode
  // 是 none、domain 是空串，此时 isWildcard 的结果没有意义，先问这一句再用。
  function hasBase(p) { return !!(p && p.domain && p.domainMode !== "none"); }

  function isWildcard(p) { return !p || p.domainMode !== "single"; }

  // domainLabel 是界面上代表这台服务器的域名文案。
  function domainLabel(p) {
    if (!p) return "";
    if (!hasBase(p)) return t(NO_BASE);
    return isWildcard(p) ? "*." + p.domain : p.domain;
  }

  // baseKindLabel 是底座在清单与徽章里的类别名。
  function baseKindLabel(p) {
    if (!hasBase(p)) return t("无底座");
    return isWildcard(p) ? t("泛域名") : t("单域名");
  }

  // baseNote 一句话说清这台服务器的底座眼下是什么状况。
  function baseNote(p) {
    if (!hasBase(p)) return t("没有底座，隧道各绑独立域名");
    return isWildcard(p) ? t("三级域名随便用") : t("单域名");
  }

  /* ---------------- 顶栏 ---------------- */

  function renderTop() {
    var box = $("topStatus");
    clear(box);
    if (!state || !state.current) return;

    var meta = clientMeta(state.client);
    var conn = el("div", "conn");
    conn.appendChild(el("span", "dot " + meta.dot));

    var info = el("div", "conn-meta");
    var strong = el("b", null, meta.label);
    info.appendChild(strong);
    info.appendChild(document.createTextNode("  ·  " + domainLabel(state.current)));
    conn.appendChild(info);
    box.appendChild(conn);

    if (state.profiles.length > 1) {
      var sel = el("select", "btn btn-sm");
      state.profiles.forEach(function (p) {
        var o = el("option", null, p.serverIp + "  ·  " + domainLabel(p));
        o.value = p.name;
        if (p.current) o.selected = true;
        sel.appendChild(o);
      });
      sel.onchange = function () {
        toast(t("正在切换到 ") + sel.value + " …");
        post("/api/profiles/" + encodeURIComponent(sel.value) + "/activate")
          .then(function (r) {
            toast(r.ok ? t("已切换并连上") : (t(r.message) || t("已切换，但未登录成功")), r.ok ? "ok" : "bad");
            refresh();
          })
          .catch(fail);
      };
      box.appendChild(sel);
    }
  }

  /* ---------------- 隧道 ---------------- */

  function renderStats() {
    var grid = $("statsGrid");
    clear(grid);
    if (!state.current) return;

    var meta = clientMeta(state.client);
    var live = state.tunnels.filter(function (tn) { return tn.localUp; }).length;

    var owned = state.tunnels.filter(function (tn) { return tn.customDomain; }).length;

    [
      {
        k: t("连接状态"),
        dot: meta.dot,
        v: meta.label,
        s: state.current.serverIp + ":" + state.current.serverPort,
        // 状态每几秒自己刷新一次，改配置得是明确一按，不能整卡片一碰就弹
        action: { label: t("编辑"), tip: t("修改服务器地址、端口与连接密钥"), run: function () { openEditServer(state.current); } }
      },
      {
        k: t("隧道"),
        v: state.tunnels.length + t(" 条"),
        s: state.tunnels.length
          ? live + t(" 条本机有服务在跑")
          : t("还没有隧道")
      },
      {
        k: t("公网域名"),
        v: domainLabel(state.current),
        s: baseNote(state.current) + (owned ? t("  ·  独立域名 ") + owned + t(" 个") : ""),
        // 必须包一层：run 是直接挂到 onclick 上的，裸传函数会把 click 事件当成档案
        action: {
          label: t("域名"),
          tip: t("查看这台服务器对外的域名，泛域名底座空着时可以在里面删掉"),
          run: function () { openDomains(state.current); }
        }
      },
      {
        k: t("开机自启"),
        dot: state.autostart ? "ok" : "off",
        v: state.autostart ? t("已开启") : t("未开启"),
        s: state.autostart ? t("开机后自动恢复隧道") : t("重启后需手动打开")
      }
    ].forEach(function (item) {
      var box = el("div", "stat");
      var head = el("div", "stat-head");
      head.appendChild(el("div", "stat-k", item.k));
      if (item.action) {
        var btn = el("button", "btn btn-sm", item.action.label);
        btn.type = "button";
        btn.title = item.action.tip;
        btn.onclick = item.action.run;
        head.appendChild(btn);
      }
      box.appendChild(head);
      var v = el("div", "stat-v");
      if (item.dot) v.appendChild(el("span", "dot " + item.dot));
      v.appendChild(el("span", null, item.v));
      box.appendChild(v);
      box.appendChild(el("div", "stat-s", item.s));
      grid.appendChild(box);
    });
  }

  function renderTunnels() {
    var sub = $("tunnelSub");
    var list = $("tunnelList");
    clear(list);
    if (!state.current) return;

    renderStats();

    sub.textContent = !hasBase(state.current)
      ? t("这台服务器没有底座，每条隧道各绑一个自己的域名；也可以在「新增隧道」里建一个泛域名底座")
      : isWildcard(state.current)
        ? t("本机端口映射到 https://<名字>.") + state.current.domain + t("，也可以单绑自己的域名")
        : t("本机端口映射到 https://") + state.current.domain + t("，它只能指向一个端口，再开就得单绑域名");

    var meta = clientMeta(state.client);
    if (state.client.state !== "running") {
      var banner = el("div", "banner " + (state.client.state === "stopped" ? "" : "bad"));
      banner.appendChild(el("span", "dot " + meta.dot));
      var txt = el("div");
      txt.appendChild(el("strong", null, meta.label));
      var detail = t(state.client.lastError) ||
        (state.client.state === "stopped"
          ? t("客户端已停止，隧道全部断开。到「设置」里重新开启。")
          : t("隧道暂时不可用。"));
      txt.appendChild(el("div", "muted", detail));
      banner.appendChild(txt);
      list.appendChild(banner);
    } else if (state.client.lastError) {
      // 登录上了不等于隧道注册成功，这条警告专门用来暴露那种「假连上」
      var warn = el("div", "banner warn");
      warn.appendChild(el("span", "dot warn"));
      var wtxt = el("div");
      wtxt.appendChild(el("strong", null, t("已连上服务器，但隧道有问题")));
      wtxt.appendChild(el("div", "muted", t(state.client.lastError)));
      warn.appendChild(wtxt);
      list.appendChild(warn);
    }

    if (!state.tunnels.length) {
      list.appendChild(el("div", "placeholder", t("还没有隧道。点右上角「新增隧道」，把本机端口映射到一个三级域名。")));
      return;
    }

    // 绿=现在真能访问；红=隧道就位但本机没服务；灰=客户端整体停了
    var running = state.client.state === "running";
    var card = el("div", "card");
    state.tunnels.forEach(function (tn) {
      var row = el("div", "row");
      row.appendChild(el("span", "dot " + (!running ? "off" : tn.localUp ? "ok" : "bad")));

      var main = el("div", "row-main");
      var a = el("a", "row-url", tn.url);
      a.href = tn.url;
      a.target = "_blank";
      a.rel = "noreferrer noopener";
      main.appendChild(a);
      var detail = tn.localUp
        ? t("本机 ") + tn.localPort + t(" 端口有服务在跑，这个地址现在能访问")
        : t("本机 ") + tn.localPort + t(" 端口还没起服务，访问会拿到 404");
      if (tn.customDomain) {
        detail = t("独立域名  ·  ") + detail;
      }
      main.appendChild(el("div", "row-sub", detail));
      row.appendChild(main);

      // 端口徽章直接指向本机地址，方便对照「公网这条通不通」和「本机服务起没起」
      var local = "http://127.0.0.1:" + tn.localPort;
      var chip = el("a", "port-chip", ":" + tn.localPort);
      chip.href = local;
      chip.target = "_blank";
      chip.rel = "noreferrer noopener";
      chip.title = t("在浏览器打开 ") + local;
      row.appendChild(chip);

      var actions = el("div", "row-actions");
      if (state.accessLog) {
        var logBtn = el("button", "btn btn-sm", t("日志"));
        logBtn.type = "button";
        logBtn.onclick = function () { openTunnelLog(tn); };
        actions.appendChild(logBtn);
      }
      var copyBtn = el("button", "btn btn-sm", t("复制"));
      copyBtn.onclick = function () {
        copyText(tn.url).then(function () { toast(t("地址已复制"), "ok"); }).catch(fail);
      };
      var delBtn = el("button", "btn btn-sm btn-danger", t("删除"));
      delBtn.onclick = function () { confirmDeleteTunnel(tn); };
      actions.appendChild(copyBtn);
      actions.appendChild(delBtn);
      row.appendChild(actions);

      card.appendChild(row);
    });
    list.appendChild(card);
  }

  function confirmDeleteTunnel(tn) {
    var p = state.current;
    // 单域名底座只挂得下一条隧道，删了它就空了，后端会顺手把底座一并收回。
    // 这是删这一条隧道的连带后果，不当场说清楚就成了背着用户改档案。
    var freesBase = hasBase(p) && !isWildcard(p) && !tn.customDomain;

    openModal({
      title: t("删除隧道"),
      subtitle: tn.url,
      body: function (box) {
        box.appendChild(el("p", "muted", t("删除后这个公网地址立即失效，本机服务不受影响。")));
        if (!freesBase) return;
        var warn = el("div", "banner warn");
        var txt = el("div");
        txt.appendChild(el("strong", null, t("单域名底座 ") + p.domain + t(" 也会一并收回")));
        txt.appendChild(el("div", "muted",
          t("它只挂得下这一条隧道，删完就空了。之后还想用这个地址，新增隧道时把它当「独立域名」绑上即可，") +
          t("效果完全一样，服务器那边不用改。")));
        warn.appendChild(txt);
        box.appendChild(warn);
      },
      confirmText: t("确认删除"),
      danger: true,
      onConfirm: function (done) {
        del("/api/tunnels/" + tn.localPort)
          .then(function () { toast(t("已删除"), "ok"); closeModal(); refresh(); })
          .catch(function (e) { fail(e); done(); });
      }
    });
  }

  // paintCustomDomainTodo 在给定容器里列出独立域名要在服务器那边补齐的三件事。
  // 这三件事面板做不了，但不说清楚，用户填完只会拿到证书报错或 404。
  function paintCustomDomainTodo(box, p, domain) {
    clear(box);
    var d = domain || t("<你的域名>");
    var txt = el("div");
    txt.appendChild(el("strong", null, t("这个域名还要在服务器那边配三件事")));
    [
      t("解析：给 ") + d + t(" 加一条 A 记录，指向 ") + p.serverIp,
      t("证书：给它单独签一张证书，普通的 HTTP 验证就够（泛域名证书盖不到它）"),
      t("站点：nginx 绑定这个域名，反代到 http://127.0.0.1:") + p.vhostPort +
        t("，并保留 proxy_set_header Host $host")
    ].forEach(function (s) { txt.appendChild(el("div", "muted", "· " + s)); });
    box.appendChild(txt);
  }

  // paintNewBaseTodo 列出新建泛域名底座之后，服务器那边必须补齐的几件事。
  //
  // 前三件是任何泛域名都要做的。第四件最容易漏，漏了的后果也最隐蔽：
  // frps 的 subDomainHost 还是旧值（或者压根没有），frpc 报上去的三级域名
  // 会被拼到另一个后缀上，面板显示的地址和实际生效的地址对不上，
  // 而两边日志都显示「proxy 启动成功」，光看现象根本反推不回这里。
  function paintNewBaseTodo(box, p, base) {
    clear(box);
    var d = base || t("<底座域名>");
    var txt = el("div");
    txt.appendChild(el("strong", null, t("新底座要在服务器那边配四件事")));
    if (hasBase(p) && !isWildcard(p)) {
      var used = state.tunnels.some(function (tn) { return !tn.customDomain; });
      txt.appendChild(el("div", "muted", used
        ? t("· 当前单域名 ") + p.domain + t(" 会转成独立域名隧道，原地址和端口都保留")
        : t("· 当前单域名 ") + p.domain + t(" 还没挂隧道，会让出底座位置")));
    }
    [
      t("解析：加两条 A 记录指向 ") + p.serverIp + "　—　" + d + t(" 和 *.") + d,
      t("证书：给 *.") + d + t(" 签一张通配符证书，只能走 DNS 验证"),
      t("站点：nginx 把这两个域名都绑上，反代到 http://127.0.0.1:") + p.vhostPort +
        t("，并保留 proxy_set_header Host $host"),
      t("脚本：到「服务端部署」页复制新脚本重跑一遍——不跑这一步，frps 认的还是旧底座，") +
        t("这条隧道会挂到一个你没要过的地址上")
    ].forEach(function (s) { txt.appendChild(el("div", "muted", "· " + s)); });
    box.appendChild(txt);
  }

  // normalizeDomain 与后端 store.NormalizeDomain 同一套规矩：去掉顺手粘进来的
  // *. 前缀与首尾的点，统一小写。前端先规整一遍，预览的地址才和最终落盘的一致。
  function normalizeDomain(raw) {
    return String(raw || "").trim().toLowerCase()
      .replace(/^\*\./, "").replace(/^\./, "").replace(/\.$/, "");
  }

  var SUBDOMAIN_RE = /^[A-Za-z0-9][A-Za-z0-9-]*$/;

  /* openAddTunnel 加一条隧道，并在这里决定它的公网地址从哪来。
     三条路互斥：挂现有底座、现建一个泛域名底座、绑一个独立域名。
     一台 frps 只有一个 subDomainHost，所以底座全服务器唯一。没有底座时可以
     直接建；单域名已经承载隧道时，先把原地址转成独立域名再建立新底座。 */
  function openAddTunnel() {
    if (!state.current) return;
    var p = state.current;
    var based = hasBase(p);
    var wildcard = based && isWildcard(p);
    var canBuildWildcard = !based || !wildcard;
    // 有底座就默认挂底座，什么都不用配；没底座默认引导用户建一个泛域名底座
    var bind = !based ? "newbase" : (wildcard ? "base" : "single");
    var portInput, subInput, domainInput, newSubInput, newBaseInput;

    openModal({
      title: t("新增隧道"),
      subtitle: t("把本机的一个端口映射到一个公网地址"),
      body: function (box) {
        var f1 = el("div", "field");
        f1.appendChild(el("label", null, t("本机端口")));
        portInput = el("input", "input");
        portInput.type = "number";
        portInput.placeholder = t("例如 3000");
        portInput.min = "1";
        portInput.max = "65535";
        f1.appendChild(portInput);
        f1.appendChild(el("div", "hint", t("你本地服务监听的端口，比如前端开发服务器的 3000、后端的 8080。")));
        box.appendChild(f1);

        var f2 = el("div", "field");
        f2.appendChild(el("label", null, t("公网地址")));
        // 底座是这台服务器唯一的、全局的那个域名，先把它摆出来，
        // 用户才知道下面那个「三级域名」到底会拼到哪儿去
        if (based) {
          var cur = el("div", "base-chip");
          cur.appendChild(el("span", "base-chip-k", t("当前底座")));
          cur.appendChild(el("b", null, domainLabel(p)));
          cur.appendChild(el("span", "port-chip", baseKindLabel(p)));
          f2.appendChild(cur);
        }
        var seg = el("div", "seg");
        f2.appendChild(seg);
        box.appendChild(f2);

        // 挂现有底座那一支：只填三级域名
        var baseField = el("div", "field");
        baseField.appendChild(el("label", null, wildcard ? t("三级域名") : t("固定域名")));
        subInput = el("input", "input");
        subInput.placeholder = t("留空则用端口号");
        var baseHint = el("div", "hint");
        if (wildcard) {
          baseField.appendChild(subInput);
        }
        baseField.appendChild(baseHint);
        box.appendChild(baseField);

        // 现建底座那一支：三级域名与底座域名并排，拼起来就是这条隧道的地址
        var newBaseField = el("div", "field");
        newBaseField.appendChild(el("label", null, t("泛域名底座")));
        var pair = el("div", "form-row");
        newSubInput = el("input", "input narrow");
        newSubInput.placeholder = "api";
        newBaseInput = el("input", "input");
        newBaseInput.placeholder = "cpolar.yourdomain.com";
        pair.appendChild(newSubInput);
        pair.appendChild(el("span", "joiner", "."));
        pair.appendChild(newBaseInput);
        newBaseField.appendChild(pair);
        var newBaseHint = el("div", "hint");
        newBaseField.appendChild(newBaseHint);
        box.appendChild(newBaseField);

        var domainField = el("div", "field");
        domainField.appendChild(el("label", null, t("独立域名")));
        domainInput = el("input", "input");
        domainInput.placeholder = t("例如 www.xxx.com");
        domainField.appendChild(domainInput);
        var domainHint = el("div", "hint");
        domainField.appendChild(domainHint);
        box.appendChild(domainField);

        var todo = el("div", "banner warn");
        box.appendChild(todo);

        var options = based
          ? [{ id: wildcard ? "base" : "single", label: wildcard ? t("泛域名下的三级域名") : t("档案域名") }]
          : [];
        if (canBuildWildcard) {
          options.push({ id: "newbase", label: t("泛域名（新建底座）") });
        }
        options.push({ id: "domain", label: t("独立域名") });
        options.forEach(function (opt) {
          var b = el("button", null, opt.label);
          b.type = "button";
          b.dataset.bind = opt.id;
          b.onclick = function () { bind = opt.id; paint(); };
          seg.appendChild(b);
        });

        function paint() {
          Array.prototype.forEach.call(seg.children, function (b) {
            b.classList.toggle("is-active", b.dataset.bind === bind);
          });
          var custom = bind === "domain";
          var newbase = bind === "newbase";
          baseField.hidden = custom || newbase;
          newBaseField.hidden = !newbase;
          domainField.hidden = !custom;
          todo.hidden = !custom && !newbase;

          var confirmBtn = $("modalBox").querySelector(".modal-confirm");
          if (confirmBtn) {
            confirmBtn.textContent = newbase ? t("建底座并创建隧道") : t("创建并连接");
          }

          if (custom) {
            var d = normalizeDomain(domainInput.value);
            domainHint.textContent = t("最终地址：https://") + (d || t("<你的域名>")) +
              (wildcard ? t("　·　不能落在 *.") + p.domain + t(" 下面，那片地方走三级域名") : "");
            paintCustomDomainTodo(todo, p, d);
            return;
          }
          if (newbase) {
            var nb = normalizeDomain(newBaseInput.value);
            var ns = newSubInput.value.trim() || portInput.value.trim() || t("<名字>");
            newBaseHint.textContent = t("最终地址：https://") + ns + "." + (nb || t("<底座域名>")) +
              t("　·　底座建成 *.") + (nb || t("<底座域名>")) + t("，之后再加隧道只要起个名字");
            paintNewBaseTodo(todo, p, nb);
            return;
          }
          if (!wildcard) {
            baseHint.textContent = t("地址固定是 https://") + p.domain +
              t("，不用起名字。要同时开多条，给别的隧道各绑一个独立域名。");
            return;
          }
          var s = subInput.value.trim() || portInput.value.trim() || t("<名字>");
          baseHint.textContent = t("最终地址：https://") + s + "." + p.domain +
            t("　·　解析和证书都是现成的，不用再配");
        }

        portInput.oninput = subInput.oninput = domainInput.oninput = paint;
        newSubInput.oninput = newBaseInput.oninput = paint;
        paint();
        setTimeout(function () { portInput.focus(); }, 50);
      },
      confirmText: t("创建并连接"),
      onConfirm: function (done) {
        var port = parseInt(portInput.value, 10);
        if (!port || port < 1 || port > 65535) {
          toast(t("端口必须是 1-65535 的数字"), "bad");
          return done();
        }
        if (bind === "newbase") {
          return createWithNewBase(p, port, newSubInput.value.trim(),
            normalizeDomain(newBaseInput.value), done);
        }
        var custom = bind === "domain" ? normalizeDomain(domainInput.value) : "";
        if (bind === "domain" && custom.indexOf(".") < 0) {
          toast(t("填一个完整域名，例如 www.xxx.com"), "bad");
          return done();
        }
        createTunnel({
          localPort: port,
          subdomain: bind === "base" ? subInput.value.trim() : "",
          customDomain: custom,
          expectedProfile: p.name
        }, done);
      }
    });
  }

  function createTunnel(body, done) {
    return post("/api/tunnels", body)
      .then(function (r) {
        closeModal();
        toast(r.ok ? t("隧道已创建") : (t(r.message) || t("已写入配置，但客户端未连上")), r.ok ? "ok" : "bad");
        refresh();
      })
      .catch(function (e) { fail(e); done(); });
  }

  /* createWithNewBase 先把底座建起来，再挂这条隧道。单域名底座也走这里：
     空着的直接替换；已经挂了隧道的先把原地址改成独立域名，不能让它凭空消失。

     两步分开发是有意的：建底座走的是改档案那条路，它会重算所有隧道的地址、
     校验冲突、写配置再重连，这套已经调稳的逻辑不该在加隧道的接口里再实现一遍。
     代价是中间可能断在两步之间，所以第二步失败时要明确交代底座已经建好了——
     悄悄退回去只会让用户对着一个「已经有底座」的界面重试，还不知道为什么。
     两步都锁定同一台服务器：第一步路径里带档案名，第二步靠 expectedProfile
     让后端核对，中途被切走时宁可当场拒绝，也不能把底座和隧道分到两台机器上。 */
  function createWithNewBase(p, port, sub, base, done) {
    if (base.indexOf(".") < 0) {
      toast(t("底座域名要填完整，例如 cpolar.yourdomain.com"), "bad");
      return done();
    }
    if (sub && !SUBDOMAIN_RE.test(sub)) {
      toast(t("三级域名不能带点和特殊字符，也不能以连字符开头"), "bad");
      return done();
    }
    var built = false;
    put("/api/profiles/" + encodeURIComponent(p.name), {
      domain: base, domainMode: "wildcard", preserveSingleDomain: true
    })
      .then(function () {
        built = true;
        return post("/api/tunnels", {
          localPort: port, subdomain: sub, customDomain: "", expectedProfile: p.name
        });
      })
      .then(function (r) {
        closeModal();
        toast(r.ok
          ? t("底座 *.") + base + t(" 已建好，隧道已创建；记得重跑一次部署脚本")
          : (t(r.message) || t("已写入配置，但客户端未连上")), r.ok ? "ok" : "bad");
        refresh();
      })
      .catch(function (e) {
        if (!built) { fail(e); return done(); }
        closeModal();
        toast(t("底座 *.") + base + t(" 已建好，但这条隧道没加上：") + t(e.message), "bad");
        refresh();
      });
  }

  /* ---------------- 连通检测 ---------------- */

  function stepNode(ok, title, detail) {
    var s = el("div", "check-step");
    s.appendChild(el("span", "dot " + (ok === null ? "warn" : ok ? "ok" : "bad")));
    var b = el("div", "check-body");
    b.appendChild(el("div", "check-title", title));
    if (detail) b.appendChild(el("div", "check-detail", detail));
    s.appendChild(b);
    return s;
  }

  // 两处界面共用这一份解读。hijacked 是旧后台遗留值，新版不再检测本机代理；
  // 兼容时按目标端口握手成功处理，不能再把代理状态暴露给用户。
  function tcpStep(srv) {
    switch (srv && srv.tcp && srv.tcp.result) {
      case "reachable":
      case "hijacked":
        return { ok: true, bad: false, suffix: "", detail: t("对端端口能建立连接") };
      case "unreachable":
        return { ok: false, bad: true, suffix: "",
                 detail: t("连不上，脚本没跑 / 安全组没放行 / IP 填错") };
      default:
        return { ok: null, bad: false, suffix: t("（测不准）"),
                 detail: t("没拿到端口探测结果，以下面的登录结果为准") };
    }
  }

  function paintServerCheck(srv) {
    var box = $("checkResult");
    clear(box);
    var card = el("div", "card");

    var tcp = tcpStep(srv);
    card.appendChild(stepNode(tcp.ok, t("① 服务器端口可达") + tcp.suffix, tcp.detail));

    var loginOK = srv.loginState === "running";
    card.appendChild(stepNode(loginOK,
      t("② 登录 frps 成功"),
      loginOK ? t("确认对端是本方案的 frps，密钥一致") : (t(srv.loginMessage) || t("未登录成功"))));

    var dnsResult = srv.dns ? srv.dns.result : "";
    var skipped = dnsResult === "skipped";
    var dnsOK = dnsResult === "ok" || dnsResult === "hijacked" || skipped;
    var dnsDetail = "";
    if (srv.dns) {
      if (dnsResult === "ok") dnsDetail = srv.dns.host + " → " + (srv.dns.ips || []).join(" ");
      else if (dnsResult === "hijacked") dnsDetail = "";
      else if (skipped) dnsDetail = t("这台服务器没有底座域名，解析跟着各条隧道的独立域名走，下面逐条看");
      else if (dnsResult === "missing") dnsDetail = t("查不到 A 记录");
      else dnsDetail = t("解析到 ") + (srv.dns.ips || []).join(" ") + t("，不是目标服务器");
    }
    card.appendChild(stepNode(dnsOK,
      skipped ? t("③ 域名解析（无底座，不适用）") : t("③ 域名解析指向服务器"), dnsDetail));
    box.appendChild(card);

    var kind = loginOK && dnsOK ? "ok" : (tcp.bad ? "bad" : "warn");
    box.appendChild(el("div", "verdict " + kind, t(srv.advice) || ""));
    return loginOK;
  }

  function paintCachedCheck() {
    if (!lastServerCheck) return false;
    paintServerCheck(lastServerCheck);
    if (lastTunnelChecks) renderTunnelChecks($("checkResult"), lastTunnelChecks);
    return true;
  }

  function runCheck() {
    var box = $("checkResult");
    var btn = $("btnRunCheck");
    btn.disabled = true;
    btn.textContent = t("检测中…");
    lastServerCheck = null;
    lastTunnelChecks = null;
    clear(box);
    box.appendChild(el("div", "placeholder", t("正在检测，最长约 30 秒…")));

    post("/api/check/server")
      .then(function (srv) {
        lastServerCheck = srv;
        var loginOK = paintServerCheck(srv);
        if (loginOK) {
          return post("/api/check/tunnels").then(function (r) {
            lastTunnelChecks = r.results || [];
            renderTunnelChecks($("checkResult"), lastTunnelChecks);
          });
        }
      })
      .catch(fail)
      .finally(function () {
        btn.disabled = false;
        btn.textContent = t("重新检测");
      });
  }

  function renderTunnelChecks(box, results) {
    var head = el("h2", null, t("隧道公网可达性"));
    head.style.margin = "24px 0 12px";
    box.appendChild(head);

    if (!results.length) {
      box.appendChild(el("div", "placeholder", t("还没有隧道可测。")));
      return;
    }
    var card = el("div", "card");
    results.forEach(function (r) {
      var detail = t(r.advice);
      if (r.http && r.http.statusCode) detail = "HTTP " + r.http.statusCode + " · " + detail;
      // 被本机开发服务器按主机名拦下时，隧道其实是通的，不该判成失败
      var ok = r.ok || (r.http && r.http.hostBlocked);
      card.appendChild(stepNode(ok, r.url, detail));
    });
    box.appendChild(card);
  }

  /* ---------------- 服务端部署 ---------------- */

  /* 这一页只服务已经接入的服务器：选一台，看它的完整部署方案。
     新建服务器一律走「设置」页的接入向导——两个入口都能新建的时候，
     用户在这儿改域名曾把正在用的档案改坏过，也没人说得清该走哪个。

     方案一定要按档案 ID 向后端要，绝不能拿页面上的表单参数现拼一份：
     那样密钥只能临时生成，而用户来这一页，多半正是改完密钥要重新取脚本的。
     拿一把对不上的钥匙去服务器重跑，两端就永远登录不上，且现象（一直登录
     失败）很难反推回这个原因。 */
  var deployPickedID = "";
  var deployPlan = null;

  function profileByName(list, name) {
    for (var i = 0; i < list.length; i++) {
      if (list[i].name === name) return list[i];
    }
    return null;
  }

  function renderDeploy() {
    var box = $("deployGuide");
    clear(box);

    var profiles = state.profiles || [];
    if (!profiles.length) {
      setScriptGate(false, t("还没接入任何服务器"));
      box.appendChild(emptyDeploy());
      return;
    }

    // 首次进来，或选中的那台已被删除：落回当前正在用的服务器
    if (!profileByName(profiles, deployPickedID)) {
      var cur = profiles.filter(function (p) { return p.current; })[0];
      deployPickedID = (cur || profiles[0]).name;
      deployPlan = null;
    }

    box.appendChild(scopeBanner());
    box.appendChild(profilePicker(profiles));

    var wrap = el("div", "steps");
    var planBox = el("div");
    planBox.id = "deployPlanBox";
    wrap.appendChild(planBox);
    box.appendChild(wrap);

    // 先把手上这份画出来避免闪烁，再无条件重取一次。
    // 密钥、域名可能刚被改过，这一页的价值全在于给出的是最新的那份。
    renderPlan(planBox);
    loadDeployPlan(deployPickedID);
  }

  function emptyDeploy() {
    var box = el("div", "placeholder");
    box.appendChild(el("div", null, t("还没接入任何服务器。先接入一台，再回来取它的部署脚本。")));
    var btn = el("button", "btn btn-primary", t("接入新服务器"));
    btn.style.marginTop = "12px";
    btn.onclick = startWizard;
    box.appendChild(btn);
    return box;
  }

  // scopeBanner 把这一页的边界说死：这里只看已有的，新建去向导。
  function scopeBanner() {
    var b = el("div", "banner");
    var txt = el("div");
    txt.appendChild(el("strong", null, t("这一页给已接入的服务器出部署方案")));
    txt.appendChild(el("div", "muted",
      t("改过密钥或域名之后回这里重新复制脚本，里面的密钥与本地档案一致，") +
      t("直接在服务器上重跑即可。要接入一台新服务器，去「设置」页点「接入新服务器」。")));
    b.appendChild(txt);
    return b;
  }

  function profilePicker(profiles) {
    var f = el("div", "field");
    f.appendChild(el("label", null, t("选择服务器")));
    var seg = el("div", "seg");
    profiles.forEach(function (p) {
      var b = el("button", null, p.name + (p.current ? t("（当前）") : ""));
      b.type = "button";
      b.classList.toggle("is-active", p.name === deployPickedID);
      b.onclick = function () {
        if (deployPickedID === p.name) return;
        deployPickedID = p.name;
        deployPlan = null;
        renderDeploy();
      };
      seg.appendChild(b);
    });
    f.appendChild(seg);

    var picked = profileByName(profiles, deployPickedID);
    if (picked) {
      f.appendChild(el("div", "hint",
        picked.serverIp + "　·　" + domainLabel(picked) + t("　·　接入端口 ") + picked.serverPort));
    }
    return f;
  }

  function loadDeployPlan(id) {
    get("/api/profiles/" + encodeURIComponent(id) + "/deploy-plan")
      .then(function (plan) {
        // 拉取期间用户可能已经切走，过期结果直接丢弃，别覆盖新选的那台
        if (deployPickedID !== id) return;
        deployPlan = plan;
        renderPlan($("deployPlanBox"));
        setScriptGate(true, "");
      })
      .catch(function (e) {
        if (deployPickedID !== id) return;
        deployPlan = null;
        renderPlan($("deployPlanBox"), t(e.message));
        setScriptGate(false, t(e.message) || t("方案没取到"));
      });
  }

  // setScriptGate 控制「复制部署脚本」能不能点：方案没取到就别让人复制空气。
  function setScriptGate(ready, why) {
    var btn = $("btnCopyScript");
    btn.disabled = !ready;
    btn.title = ready ? "" : why;
  }

  function renderPlan(planBox, errMsg) {
    if (!planBox) return;
    planBox.id = "deployPlanBox";
    clear(planBox);

    if (errMsg) {
      planBox.appendChild(el("div", "verdict bad", errMsg));
      return;
    }
    if (!deployPlan) {
      planBox.appendChild(el("div", "placeholder", t("正在算这台服务器的部署方案…")));
      return;
    }
    planBox.appendChild(dnsStep(deployPlan));
    planBox.appendChild(certStep(deployPlan));
    planBox.appendChild(scriptStep(deployPlan));
    planBox.appendChild(proxyStep(deployPlan));
  }

  function dnsStep(d) {
    var step = el("div", "step");
    step.appendChild(el("h3", null, t("解析域名")));
    // 无底座的档案没有解析记录可下发，后端给的是空列表
    var recs = d.dnsRecords || [];
    if (!recs.length) {
      step.appendChild(el("p", null,
        t("这台服务器没有底座域名，不用为它加解析。各条隧道自己绑的独立域名，") +
        t("各加一条 A 记录指向 ") + d.ip + t(" 即可。")));
      return step;
    }
    step.appendChild(el("p", null, recs.length > 1
      ? t("到域名服务商后台加这两条 A 记录，都指向你的服务器。泛解析那条不能少，它让所有三级域名都能用。")
      : t("到域名服务商后台加这条 A 记录，指向你的服务器。")));

    var table = el("table", "rec-table");
    var head = el("tr");
    [t("主机记录"), t("记录类型"), t("记录值"), t("对应完整域名")].forEach(function (tn) {
      head.appendChild(el("th", null, tn));
    });
    table.appendChild(head);
    recs.forEach(function (rec) {
      var tr = el("tr");
      [rec.host, rec.type, rec.value, rec.fqdn].forEach(function (tn) {
        tr.appendChild(el("td", null, tn));
      });
      table.appendChild(tr);
    });
    step.appendChild(table);
    step.appendChild(el("div", "hint",
      t("「主机记录」是按根域 ") + d.rootDomain + t(" 推算的。你的服务商如果是按完整域名填，直接照最后一列填。")));
    return step;
  }

  function certStep(d) {
    var step = el("div", "step");
    var sites = d.siteDomains || [];
    if (!sites.length) {
      step.appendChild(el("h3", null, t("建站并申请证书")));
      step.appendChild(el("p", null, t(d.certNote)));
      return step;
    }
    step.appendChild(el("h3", null, sites.length > 1 ? t("建站并申请通配符证书") : t("建站并申请证书")));
    step.appendChild(el("p", null,
      t("在服务器上建一个站点，域名填") + (sites.length > 1 ? t("两行：") : t("：")) +
      sites.join(t(" 和 ")) + t("。") + t(d.certNote)));
    return step;
  }

  function scriptStep(d) {
    var step = el("div", "step");
    step.appendChild(el("h3", null, t("跑部署脚本")));
    step.appendChild(el("p", null, t("复制下面的脚本，粘到服务器终端里执行。它会装好 frps、写配置、注册开机自启。")));
    step.appendChild(el("div", "code", d.script));
    var note = el("p");
    note.style.marginTop = "9px";
    note.textContent = t("脚本里已经写好了本机的连接密钥，直接跑即可，不用改任何东西。也已存到 ") + d.path;
    step.appendChild(note);
    return step;
  }

  function proxyStep(d) {
    var step = el("div", "step");
    step.appendChild(el("h3", null, t("放行端口并配置反向代理")));
    var p = el("p");
    p.innerHTML = t("云服务商安全组放行 <b class='mono'>") + d.port + t("/TCP</b>（") + d.vhost +
      t(" 不要对外开放）。先在站点里添加反向代理，再把它的 <b class='mono'>location / {...}</b> ") +
      t("整块替换成下面这份配置。它已包含 Host 分流、WebSocket、长任务、大请求与 SSE 支持。");
    step.appendChild(p);
    step.appendChild(el("div", "code", d.nginxConfig));
    var copyBtn = el("button", "btn btn-ghost", t("复制 nginx 配置"));
    copyBtn.type = "button";
    copyBtn.style.marginTop = "10px";
    copyBtn.onclick = function () {
      copyText(d.nginxConfig)
        .then(function () { toast(t("nginx 配置已复制，去站点反向代理配置里替换 location"), "ok"); })
        .catch(fail);
    };
    step.appendChild(copyBtn);
    return step;
  }

  // copyDeployScript 复制选中那台服务器的脚本，与页面上看到的完全一致。
  function copyDeployScript() {
    if (!deployPlan) {
      return toast(t("方案还没取到，稍等一下再点"), "bad");
    }
    copyText(deployPlan.script)
      .then(function () { toast(t("部署脚本已复制，去服务器终端粘贴执行"), "ok"); })
      .catch(fail);
  }

  // copyScript 复制当前档案的脚本，供接入向导使用。
  function copyScript() {
    get("/api/deploy-script")
      .then(function (d) { return copyText(d.script); })
      .then(function () { toast(t("部署脚本已复制，去服务器终端粘贴执行"), "ok"); })
      .catch(fail);
  }

  /* ---------------- 日志 ---------------- */

  function openTunnelLog(tn) {
    logKind = "tunnel";
    selectedLogPort = tn.localPort;
    showView("logs");
  }

  function paintLogChrome() {
    var kinds = $("logKinds");
    var title = $("logTitle");
    var nav = $("logTunnelNav");
    var pluginOn = !!(state && state.accessLog);
    if (!pluginOn) logKind = "client";
    kinds.hidden = !pluginOn;
    title.hidden = pluginOn;
    if (!pluginOn) title.textContent = t("客户端日志");
    Array.prototype.forEach.call(kinds.querySelectorAll(".log-kind"), function (btn) {
      btn.classList.toggle("is-active", btn.dataset.logKind === logKind);
    });
    nav.hidden = !(pluginOn && logKind === "tunnel");
    if (pluginOn && logKind === "tunnel") renderLogTunnelNav();
  }

  function renderLogTunnelNav() {
    var nav = $("logTunnelNav");
    clear(nav);
    var tunnels = (state && state.tunnels) || [];
    if (!tunnels.length) {
      nav.appendChild(el("div", "placeholder", t("还没有隧道")));
      selectedLogPort = 0;
      return;
    }
    var stillThere = tunnels.some(function (tn) { return tn.localPort === selectedLogPort; });
    if (!stillThere) selectedLogPort = tunnels[0].localPort;
    tunnels.forEach(function (tn) {
      var btn = el("button", "log-tunnel-item" + (tn.localPort === selectedLogPort ? " is-active" : ""), "");
      btn.type = "button";
      btn.appendChild(el("div", "log-tunnel-host", tn.host || tn.url));
      btn.appendChild(el("div", "log-tunnel-port", ":" + tn.localPort));
      btn.onclick = function () {
        selectedLogPort = tn.localPort;
        loadLog();
      };
      nav.appendChild(btn);
    });
  }

  function loadLog() {
    if (currentView !== "logs") return;
    paintLogChrome();
    if (state && state.accessLog && logKind === "tunnel") {
      loadTunnelAccessLog();
      return;
    }
    get("/api/logs?lines=300").then(function (d) {
      $("logPath").textContent = d.path || "";
      var box = $("logBox");
      var atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
      box.textContent = d.log || t("（暂无日志）");
      if (atBottom) box.scrollTop = box.scrollHeight;
    }).catch(function () { /* 日志拉取失败不打扰用户 */ });
  }

  function loadTunnelAccessLog() {
    if (!selectedLogPort) {
      $("logPath").textContent = "";
      $("logBox").textContent = t("选择一条隧道查看访问日志");
      return;
    }
    get("/api/plugins/access-log/tunnels/" + selectedLogPort + "/log?lines=300").then(function (d) {
      $("logPath").textContent = d.path || "";
      var box = $("logBox");
      var atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
      box.textContent = d.log || t("（暂无访问日志。公网打到这条隧道的请求会出现在这里。）");
      if (atBottom) box.scrollTop = box.scrollHeight;
    }).catch(function () { /* 日志拉取失败不打扰用户 */ });
  }

  function syncLogTimer() {
    clearInterval(logTimer);
    logTimer = null;
    if (currentView === "logs" && $("logAuto").checked) {
      logTimer = setInterval(loadLog, 2500);
    }
  }

  /* ---------------- 设置 ---------------- */

  function renderSettings() {
    var box = $("settingsBody");
    clear(box);
    var card = el("div", "card");

    var loc = currentLocale();
    var sLang = el("div", "setting");
    var mLang = el("div", "setting-main");
    mLang.appendChild(el("h3", null, "Language / 语言"));
    mLang.appendChild(el("p", null, t("控制台显示语言，切换后立即生效。")));
    sLang.appendChild(mLang);
    var langSeg = el("div", "seg");
    [
      { id: "en", label: "English" },
      { id: "zh-CN", label: "中文" }
    ].forEach(function (opt) {
      var b = el("button", null, opt.label);
      b.type = "button";
      b.setAttribute("data-locale", opt.id);
      b.classList.toggle("is-active", loc === opt.id);
      langSeg.appendChild(b);
    });
    sLang.appendChild(langSeg);
    card.appendChild(sLang);

    // 客户端开关
    var meta = clientMeta(state.client);
    var s1 = el("div", "setting");
    var m1 = el("div", "setting-main");
    var h1 = el("h3");
    h1.appendChild(el("span", "dot " + meta.dot));
    h1.appendChild(document.createTextNode(t(" 隧道客户端 · ") + meta.label));
    m1.appendChild(h1);
    m1.appendChild(el("p", null, t(state.client.lastError) || t("关闭后所有隧道立即断开，本地服务仍在运行。")));
    s1.appendChild(m1);
    if (state.client.state === "stopped") {
      var onBtn = el("button", "btn btn-primary", t("开启"));
      onBtn.onclick = function () { clientAction("start", onBtn); };
      s1.appendChild(onBtn);
    } else {
      var reBtn = el("button", "btn", t("重启"));
      reBtn.onclick = function () { clientAction("restart", reBtn); };
      var offBtn = el("button", "btn btn-danger", t("关闭"));
      offBtn.onclick = function () { clientAction("stop", offBtn); };
      s1.appendChild(reBtn);
      s1.appendChild(offBtn);
    }
    card.appendChild(s1);

    // 开机自启
    var s2 = el("div", "setting");
    var m2 = el("div", "setting-main");
    m2.appendChild(el("h3", null, t("开机自动启动")));
    m2.appendChild(el("p", null, t("开启后每次开机自动运行，浏览器直接输网址就能打开这个页面，不用再点启动器。")));
    s2.appendChild(m2);
    s2.appendChild(toggleNode(state.autostart, function (val, input) {
      post("/api/autostart", { enabled: val })
        .then(function (r) {
          toast(r.autostart ? t("已开启开机自启") : t("已关闭开机自启（本次运行不受影响）"), "ok");
          refresh();
        })
        .catch(function (e) { fail(e); input.checked = !val; });
    }));
    card.appendChild(s2);

    // 控制台地址
    var s3 = el("div", "setting");
    var m3 = el("div", "setting-main");
    m3.appendChild(el("h3", null, t("控制台地址")));
    m3.appendChild(el("p", null, "http://127.0.0.1:" + state.port + t(" — 收藏它，以后直接打开。")));
    s3.appendChild(m3);
    var copyAddr = el("button", "btn", t("复制地址"));
    copyAddr.onclick = function () {
      copyText("http://127.0.0.1:" + state.port + "/?token=" + token)
        .then(function () { toast(t("已复制带令牌的地址"), "ok"); })
        .catch(fail);
    };
    s3.appendChild(copyAddr);
    card.appendChild(s3);

    box.appendChild(card);

    // 服务器档案
    var head2 = el("div", "section-head");
    head2.style.marginTop = "26px";
    var hd = el("div");
    hd.appendChild(el("h2", null, t("服务器")));
    hd.appendChild(el("p", "section-sub", t("同一时刻只连一台。切换会重连隧道。")));
    head2.appendChild(hd);
    var addBtn = el("button", "btn btn-primary", t("+ 接入新服务器"));
    addBtn.onclick = startWizard;
    head2.appendChild(addBtn);
    box.appendChild(head2);

    var card2 = el("div", "card");
    state.profiles.forEach(function (p) {
      var row = el("div", "row");
      row.appendChild(el("span", "dot " + (p.current ? "ok" : "off")));
      var main = el("div", "row-main");
      main.appendChild(el("div", "row-url", p.serverIp));
      main.appendChild(el("div", "row-sub", domainLabel(p) + t("  ·  端口 ") + p.serverPort));
      row.appendChild(main);

      var acts = el("div", "row-actions");
      var editBtn = el("button", "btn btn-sm", t("编辑"));
      editBtn.title = t("修改服务器地址、端口与连接密钥");
      editBtn.onclick = function () { openEditServer(p); };
      acts.appendChild(editBtn);
      var domainBtn = el("button", "btn btn-sm", t("域名"));
      domainBtn.title = t("查看这台服务器对外的域名，泛域名底座空着时可以在里面删掉");
      domainBtn.onclick = function () { openDomains(p); };
      acts.appendChild(domainBtn);
      if (!p.current) {
        var useBtn = el("button", "btn btn-sm", t("切换"));
        useBtn.onclick = function () {
          useBtn.disabled = true;
          post("/api/profiles/" + encodeURIComponent(p.name) + "/activate")
            .then(function (r) {
              toast(r.ok ? t("已切换并连上") : (t(r.message) || t("已切换，但未登录成功")), r.ok ? "ok" : "bad");
              refresh();
            })
            .catch(fail);
        };
        acts.appendChild(useBtn);
      } else {
        acts.appendChild(el("span", "port-chip", t("当前")));
      }
      var rmBtn = el("button", "btn btn-sm btn-danger", t("删除"));
      rmBtn.onclick = function () { confirmDeleteProfile(p); };
      acts.appendChild(rmBtn);
      row.appendChild(acts);
      card2.appendChild(row);
    });
    box.appendChild(card2);

    // 危险操作
    var card3 = el("div", "card");
    card3.style.marginTop = "26px";
    var s4 = el("div", "setting");
    var m4 = el("div", "setting-main");
    m4.appendChild(el("h3", null, t("停止本地服务")));
    m4.appendChild(el("p", null, t("彻底停掉后台服务，所有隧道断开，这个页面也会失联。重新双击启动器即可恢复。")));
    s4.appendChild(m4);
    var stopBtn = el("button", "btn btn-danger", t("停止服务"));
    stopBtn.onclick = confirmStopService;
    s4.appendChild(stopBtn);
    card3.appendChild(s4);

    var s5 = el("div", "setting");
    var m5 = el("div", "setting-main");
    m5.appendChild(el("h3", null, t("关于")));
    m5.appendChild(el("p", null,
      t("控制台 ") + state.version + "  ·  " + (state.frpcVersion || t("frpc 未就绪")) +
      t("  ·  配置目录 ") + state.dataDir));
    s5.appendChild(m5);
    card3.appendChild(s5);
    box.appendChild(card3);
  }

  function toggleNode(checked, onChange) {
    var wrap = el("label", "toggle");
    var input = el("input");
    input.type = "checkbox";
    input.checked = !!checked;
    input.onchange = function () { onChange(input.checked, input); };
    wrap.appendChild(input);
    wrap.appendChild(el("span", "toggle-track"));
    return wrap;
  }

  function clientAction(action, btn) {
    btn.disabled = true;
    var origin = btn.textContent;
    btn.textContent = t("处理中…");
    post("/api/client/" + action)
      .then(function (r) {
        toast(r.ok ? t("操作完成") : (t(r.message) || t("操作完成，但客户端未连上")), r.ok ? "ok" : "bad");
        refresh();
      })
      .catch(function (e) {
        fail(e);
        btn.disabled = false;
        btn.textContent = origin;
      });
  }

  // saveProfile 提交档案改动。没填的字段后端一律当作不改，
  // 所以「编辑服务器」和「修改域名」两个弹窗各提交各的，不会互相冲掉。
  function saveProfile(p, patch, okMsg, done) {
    put("/api/profiles/" + encodeURIComponent(p.name), patch)
      .then(function (r) {
        closeModal();
        toast(r.ok ? okMsg : (t(r.message) || t("已保存，但客户端未连上")), r.ok ? "ok" : "bad");
        refresh();
      })
      .catch(function (e) { fail(e); done(); });
  }

  // openEditServer 改这台服务器的连接方式，不碰域名。
  function openEditServer(p) {
    var ipInput, portInput, tokenInput;

    openModal({
      title: t("编辑服务器"),
      subtitle: p.name + "  ·  " + domainLabel(p),
      body: function (box) {
        var f1 = el("div", "field");
        f1.appendChild(el("label", null, t("服务器公网 IP")));
        ipInput = el("input", "input");
        ipInput.value = p.serverIp;
        f1.appendChild(ipInput);
        box.appendChild(f1);

        var f2 = el("div", "field");
        f2.appendChild(el("label", null, t("服务端口")));
        portInput = el("input", "input");
        portInput.type = "number";
        portInput.value = p.serverPort;
        f2.appendChild(portInput);
        f2.appendChild(el("div", "hint", t("frps 的控制端口，默认 7000。改了要重新放行安全组。")));
        box.appendChild(f2);

        var f3 = el("div", "field");
        f3.appendChild(el("label", null, t("连接密钥")));
        tokenInput = el("input", "input");
        tokenInput.placeholder = t("留空表示不修改");
        f3.appendChild(tokenInput);
        f3.appendChild(el("div", "hint", t("字母、数字、下划线、连字符，8-128 位。")));
        box.appendChild(f3);

        var warn = el("div", "banner warn");
        var warnTxt = el("div");
        warnTxt.appendChild(el("strong", null, t("端口或密钥改了，服务器那边也得改")));
        warnTxt.appendChild(el("div", "muted",
          t("两端对不上就会一直登录失败。改完到「服务端部署」页选中这台服务器，") +
          t("复制新脚本在服务器上重跑一遍。")));
        warn.appendChild(warnTxt);
        box.appendChild(warn);

        // 换 IP 只让 frpc 连到新机器，公网域名还指着旧机器，这一步漏了就全站打不开
        var dns = el("div", "banner bad");
        box.appendChild(dns);
        ipInput.oninput = function () {
          var ip = ipInput.value.trim();
          // 没有底座、也没有独立域名时无解析可改，那就别摆一条空警告吓人
          var hosts = (hasBase(p) ? [domainLabel(p)] : []).concat(state.tunnels
            .filter(function (x) { return x.customDomain; })
            .map(function (x) { return x.customDomain; }));
          dns.hidden = !ip || ip === p.serverIp || !hosts.length;
          if (dns.hidden) return;
          clear(dns);
          var d = el("div");
          d.appendChild(el("strong", null, t("换了 IP，这些域名的解析也要一起改到 ") + ip));
          hosts.forEach(function (h) { d.appendChild(el("div", "muted", "· " + h)); });
          d.appendChild(el("div", "muted", t("解析没改完之前，公网地址还会打到旧服务器上。")));
          dns.appendChild(d);
        };
        ipInput.oninput();

        setTimeout(function () { ipInput.focus(); }, 50);
      },
      confirmText: t("保存并重连"),
      onConfirm: function (done) {
        var ip = ipInput.value.trim();
        if (!ip) { toast(t("服务器地址不能为空"), "bad"); return done(); }
        var port = parseInt(portInput.value, 10);
        if (!port || port < 1 || port > 65535) {
          toast(t("服务端口必须是 1-65535 的数字"), "bad");
          return done();
        }
        saveProfile(p, {
          serverIp: ip, serverPort: port, token: tokenInput.value.trim()
        }, t("连接信息已更新"), done);
      }
    });
  }

  // domainHeadRow 是清单里的域名行，底下会缩进挂它名下的隧道。
  // action 给了才挂按钮：独立域名跟着隧道走，那种行没有可点的东西。
  function domainHeadRow(label, kind, note, action) {
    var r = el("div", "row");
    var m = el("div", "row-main");
    m.appendChild(el("div", "row-url", label));
    m.appendChild(el("div", "row-sub", note));
    r.appendChild(m);
    var acts = el("div", "row-actions");
    acts.appendChild(el("span", "port-chip", kind));
    if (action) {
      var btn = el("button", "btn btn-sm btn-danger", action.label);
      btn.type = "button";
      btn.title = action.tip;
      btn.onclick = action.run;
      acts.appendChild(btn);
    }
    r.appendChild(acts);
    return r;
  }

  // tunnelSubRow 是挂在某个域名下的隧道，缩进以示从属。
  function tunnelSubRow(tn) {
    var r = el("div", "row");
    r.style.paddingLeft = "28px";
    r.appendChild(el("span", "dot " + (tn.localUp ? "ok" : "off")));
    var m = el("div", "row-main");
    m.appendChild(el("div", "row-url", tn.url));
    m.appendChild(el("div", "row-sub", tn.localUp
      ? t("本机 ") + tn.localPort + t(" 端口有服务在跑")
      : t("本机 ") + tn.localPort + t(" 端口还没起服务")));
    r.appendChild(m);
    var acts = el("div", "row-actions");
    acts.appendChild(el("span", "port-chip", ":" + tn.localPort));
    r.appendChild(acts);
    return r;
  }

  /* openDomains 列出这台服务器对外的全部域名，按域名分组、隧道缩进挂在名下。
     基本是只读的：独立域名跟着隧道走，加一条隧道就是多一个地址，删掉就是收回，
     都在「隧道」页做。唯一的例外是空着的泛域名底座——它不跟着任何隧道走，
     不在这儿给个出口就永远删不掉了。 */
  function openDomains(p) {
    p = p || state.current;
    if (!p) return;

    // state.tunnels 只有当前档案那一份，别的服务器的隧道接口压根没下发。
    // 拿它去给另一台服务器算「底座下面有没有隧道」，会算出一个凭空的空底座，
    // 让删除按钮长在一个其实还挂着隧道的底座上——点下去只会吃后端一个 400。
    var isCurrent = !!(state.current && p.name === state.current.name);
    // 接口只给真正绑了独立域名的隧道下发 customDomain，据此分两组即可
    var onBase = isCurrent ? state.tunnels.filter(function (tn) { return !tn.customDomain; }) : [];
    var owned = isCurrent ? state.tunnels.filter(function (tn) { return tn.customDomain; }) : [];
    // 底座下面还挂着隧道就不能删：删了它们全都没地址。单域名底座不给这颗按钮，
    // 它的最后一条隧道一删就自动收回，用不着用户来这里补一刀。
    var removable = isCurrent && hasBase(p) && isWildcard(p) && !onBase.length;

    openModal({
      title: t("公网域名"),
      subtitle: p.name + "  ·  " + p.serverIp,
      body: function (box) {
        var card = el("div", "card");

        if (!hasBase(p)) {
          card.appendChild(domainHeadRow(t("还没有底座域名"), t("无底座"),
            t("这台服务器不占任何档案域名。要一次配好、之后随便加三级域名，") +
            t("在「新增隧道」里选「泛域名」建一个")));
        } else {
          card.appendChild(domainHeadRow(
            domainLabel(p),
            baseKindLabel(p),
            isWildcard(p)
              ? t("隧道各占一个三级域名，解析和证书一次配好，加隧道服务器不用动")
              : t("只有这一个域名对外，底下只能挂一条隧道；那条隧道删掉时它会自动收回"),
            removable ? {
              label: t("删除"),
              tip: t("底座下面没有隧道了，可以删掉"),
              run: function () { confirmDeleteBase(p); }
            } : null));
          if (!isCurrent) {
            var off = el("div", "row");
            off.style.paddingLeft = "28px";
            off.appendChild(el("div", "row-sub", t("切换到这台服务器才能看它名下的隧道")));
            card.appendChild(off);
          } else if (onBase.length) {
            onBase.forEach(function (tn) { card.appendChild(tunnelSubRow(tn)); });
          } else {
            var none = el("div", "row");
            none.style.paddingLeft = "28px";
            none.appendChild(el("div", "row-sub", t("还没有隧道挂在它下面")));
            card.appendChild(none);
          }
        }

        owned.forEach(function (tn) {
          card.appendChild(domainHeadRow(tn.customDomain, t("独立域名"),
            t("这条隧道自己绑的，A 记录、证书、nginx 站点都要单独配")));
          card.appendChild(tunnelSubRow(tn));
        });
        box.appendChild(card);

        box.appendChild(el("div", "hint", isCurrent
          ? t("域名跟着隧道走：在「隧道」页新增一条就是多一个地址，删掉就是收回。") +
            t("想要一个完全独立的域名，新增隧道时选「独立域名」。")
          : t("这台服务器不是当前连着的那台，隧道清单与底座的删除按钮都只对当前服务器开放；") +
            t("先在顶栏切换过去再来。")));

      },
      confirmText: t("知道了"),
      hideCancel: true,
      onConfirm: function () { closeModal(); }
    });
  }

  /* confirmDeleteBase 删掉泛域名底座，把档案切到无底座。
     本地这一步只是不再往那片地址上挂隧道；真正的开关在服务器的 frps.toml 里，
     那行 subDomainHost 不去掉，frps 就一直认为这片域名是它的地盘，
     用户拿这片地址去当独立域名绑会被它直接拒收。所以脚本必须重跑，这话得说死。 */
  function confirmDeleteBase(p) {
    var base = domainLabel(p);
    openModal({
      title: t("删除泛域名底座"),
      subtitle: base,
      body: function (box) {
        box.appendChild(el("p", "muted",
          t("删掉之后这台服务器就没有档案域名了，新隧道只能各绑一个独立域名，") +
          t("直到你在「新增隧道」里再建一个底座。已有的独立域名不受影响。")));

        var warn = el("div", "banner warn");
        var txt = el("div");
        txt.appendChild(el("strong", null, t("服务器那边要跟着改一次")));
        [
          t("到「服务端部署」页复制新脚本，在服务器上重跑一遍——新脚本会去掉 frps 的 subDomainHost"),
          t("不重跑的话 frps 仍认着 ") + base + t(" 这片地址，你把它拿去当独立域名绑会被拒收"),
          t("DNS 解析和通配符证书留着不碍事，想清干净可以自己去删")
        ].forEach(function (s) { txt.appendChild(el("div", "muted", "· " + s)); });
        warn.appendChild(txt);
        box.appendChild(warn);
      },
      confirmText: t("确认删除"),
      danger: true,
      onConfirm: function (done) {
        saveProfile(p, { domainMode: "none" }, t("底座已删除，记得重跑一次部署脚本"), done);
      }
    });
  }

  function confirmDeleteProfile(p) {
    openModal({
      title: t("删除服务器"),
      subtitle: p.serverIp + "  ·  " + domainLabel(p),
      body: function (box) {
        box.appendChild(el("p", "muted",
          t("只删除本机保存的配置和这台服务器下的隧道，不会动远端服务器上的任何东西。")));
      },
      confirmText: t("确认删除"),
      danger: true,
      onConfirm: function (done) {
        del("/api/profiles/" + encodeURIComponent(p.name))
          .then(function () { toast(t("已删除"), "ok"); closeModal(); refresh(); })
          .catch(function (e) { fail(e); done(); });
      }
    });
  }

  function confirmStopService() {
    openModal({
      title: t("停止本地服务"),
      subtitle: t("所有隧道会立即断开"),
      body: function (box) {
        box.appendChild(el("p", "muted",
          t("停止后这个页面就连不上了，属于正常现象。想再用的时候，双击「frp-ngrok」启动器即可。")));
      },
      confirmText: t("停止服务"),
      danger: true,
      onConfirm: function () {
        post("/api/service/stop")
          .then(function () {
            closeModal();
            clearInterval(pollTimer);
            clearInterval(logTimer);
            document.body.innerHTML =
              t('<div class="boot"><p>本地服务已停止，隧道已全部断开。<br><br>') +
              t('<span class="muted">重新双击启动器即可恢复。</span></p></div>');
          })
          .catch(fail);
      }
    });
  }

  /* ---------------- 接入向导 ---------------- */

  function startWizard() {
    var step = 1;
    var ipInput, domainInput, localPortInput;
    var created = null;
    var mode = "wildcard";

    function render(box, foot, progress) {
      clear(box);
      clear(foot);
      clear(progress);
      for (var i = 1; i <= 3; i++) {
        progress.appendChild(el("i", i <= step ? "done" : ""));
      }

      if (step === 1) {
        var f1 = el("div", "field");
        f1.appendChild(el("label", null, t("服务器公网 IP")));
        ipInput = el("input", "input");
        ipInput.placeholder = t("例如 203.0.113.10");
        if (created) ipInput.value = created.serverIp;
        f1.appendChild(ipInput);
        f1.appendChild(el("div", "hint", t("你自己那台云服务器的公网地址。")));
        box.appendChild(f1);

        var f2 = el("div", "field");
        f2.appendChild(el("label", null, t("域名模式")));
        var seg = el("div", "seg");
        var modeHint = el("div", "hint");
        ["single", "wildcard"].forEach(function (m) {
          var b = el("button", null, m === "single" ? t("单域名") : t("泛域名"));
          b.type = "button";
          b.dataset.mode = m;
          b.onclick = function () { mode = m; paintMode(); };
          seg.appendChild(b);
        });
        f2.appendChild(seg);
        f2.appendChild(modeHint);
        box.appendChild(f2);

        var f3 = el("div", "field");
        var domainLabelEl = el("label", null, t("域名"));
        f3.appendChild(domainLabelEl);
        domainInput = el("input", "input");
        if (created) domainInput.value = created.domain;
        var domainPair = el("div", "form-row");
        domainPair.appendChild(domainInput);
        localPortInput = el("input", "input narrow");
        localPortInput.type = "number";
        localPortInput.min = "1";
        localPortInput.max = "65535";
        localPortInput.placeholder = t("本机端口，例如 3000");
        domainPair.appendChild(localPortInput);
        f3.appendChild(domainPair);
        var domainHint = el("div", "hint");
        f3.appendChild(domainHint);
        box.appendChild(f3);

        function paintMode() {
          Array.prototype.forEach.call(seg.children, function (b) {
            b.classList.toggle("is-active", b.dataset.mode === mode);
          });
          if (mode === "single") {
            modeHint.textContent = t("一个固定域名直达本机端口，普通证书就够；接入时会直接创建这条隧道。");
            domainLabelEl.textContent = t("对外域名与本机端口");
            domainInput.placeholder = t("例如 www.yourdomain.com");
            localPortInput.hidden = false;
            domainHint.textContent = t("接入完成后会直接得到 https://这个域名 → 127.0.0.1:端口。");
          } else {
            modeHint.textContent = t("一次配好，之后每条隧道自动分一个三级域名。需要一张通配符证书（DNS 验证签发）。");
            domainLabelEl.textContent = t("泛域名后缀");
            domainInput.placeholder = t("例如 tunnel.yourdomain.com");
            localPortInput.hidden = true;
            domainHint.textContent =
              t("不用带 *. 前缀。填 tunnel.yourdomain.com，就要一起配好 *.tunnel.yourdomain.com，") +
              t("隧道地址长这样：https://api.tunnel.yourdomain.com。");
          }
        }
        paintMode();

        var tip = el("div", "banner");
        tip.appendChild(el("div", null,
          t("连接密钥会自动生成并写进部署脚本，你不用填、也不用记。") +
          t("域名以后随时能在「设置」页服务器列表的「域名」按钮里改。")));
        box.appendChild(tip);

        var next = el("button", "btn btn-primary", t("下一步"));
        next.onclick = function () {
          var ip = ipInput.value.trim();
          var domain = domainInput.value.trim();
          var localPort = mode === "single" ? parseInt(localPortInput.value, 10) : 0;
          if (!ip) return toast(t("请填写服务器 IP"), "bad");
          if (domain.indexOf(".") < 0) return toast(t("请填写完整的域名"), "bad");
          if (mode === "single" && (!localPort || localPort < 1 || localPort > 65535)) {
            return toast(t("请填写 1-65535 的本机端口"), "bad");
          }
          next.disabled = true;
          post("/api/profiles", {
            serverIp: ip, domain: domain, domainMode: mode, localPort: localPort
          })
            .then(function (r) {
              created = r.profile;
              return refresh();
            })
            .then(function () {
              step = 2;
              render(box, foot, progress);
            })
            .catch(function (e) { fail(e); next.disabled = false; });
        };
        foot.appendChild(el("div", "spacer"));
        foot.appendChild(next);
        setTimeout(function () { ipInput.focus(); }, 50);
        return;
      }

      if (step === 2) {
        box.appendChild(el("p", "muted",
          isWildcard(created)
            ? t("档案已保存。接下来去把服务器那边配好——三件事，都在「服务端部署」页有详细步骤。")
            : t("档案和首条单域名隧道已保存。接下来把服务器那边的三件事配好，这个地址就能直达刚填写的本机端口。")));

        var wild = isWildcard(created);
        var ol = el("div", "steps");
        ol.style.marginTop = "16px";
        [
          [t("解析域名"), wild
            ? t("加两条 A 记录指向 ") + created.serverIp + "：" + created.domain + t(" 和 *.") + created.domain
            : t("加一条 A 记录指向 ") + created.serverIp + "：" + created.domain],
          [wild ? t("申请通配符证书") : t("申请证书"), wild
            ? t("建站绑定 ") + created.domain + t(" 和 *.") + created.domain + t("，用 DNS 验证签 Let's Encrypt 证书")
            : t("建站绑定 ") + created.domain + t("，签一张普通的 Let's Encrypt 证书即可")],
          [t("跑部署脚本"), t("复制脚本到服务器终端执行，然后放行 ") + created.serverPort +
            t("/TCP，反代到 127.0.0.1:") + created.vhostPort]
        ].forEach(function (pair) {
          var s = el("div", "step");
          s.appendChild(el("h3", null, pair[0]));
          s.appendChild(el("p", null, pair[1]));
          ol.appendChild(s);
        });
        box.appendChild(ol);

        var copyBtn = el("button", "btn", t("复制部署脚本"));
        copyBtn.onclick = function () { copyScript(); };
        var next2 = el("button", "btn btn-primary", t("都做完了，开始检测"));
        next2.onclick = function () {
          step = 3;
          render(box, foot, progress);
        };
        foot.appendChild(copyBtn);
        foot.appendChild(el("div", "spacer"));
        foot.appendChild(next2);
        return;
      }

      // 第三步：验收
      box.appendChild(el("div", "placeholder", t("正在检测服务器，最长约 30 秒…")));
      var back = el("button", "btn", t("上一步"));
      back.onclick = function () { step = 2; render(box, foot, progress); };
      var done = el("button", "btn btn-primary", t("完成"));
      done.onclick = function () { closeModal(); refresh(); };
      foot.appendChild(back);
      foot.appendChild(el("div", "spacer"));
      foot.appendChild(done);

      post("/api/check/server").then(function (srv) {
        clear(box);
        var card = el("div", "card");
        var tcp = tcpStep(srv);
        card.appendChild(stepNode(tcp.ok, t("服务器端口可达") + tcp.suffix, tcp.detail));
        var loginOK = srv.loginState === "running";
        card.appendChild(stepNode(loginOK, t("登录 frps 成功"),
          loginOK ? "" : t(srv.loginMessage || "")));
        var dnsOK = srv.dns && (srv.dns.result === "ok" || srv.dns.result === "hijacked" ||
          srv.dns.result === "skipped");
        card.appendChild(stepNode(dnsOK, t("域名解析指向服务器"), ""));
        box.appendChild(card);
        box.appendChild(el("div",
          "verdict " + (loginOK && dnsOK ? "ok" : (tcp.bad ? "bad" : "warn")), t(srv.advice) || ""));
        if (loginOK && dnsOK) done.textContent = t("完成，去加隧道");
      }).catch(function (e) {
        clear(box);
        box.appendChild(el("div", "verdict bad", t(e.message)));
      });
    }

    openModal({
      title: t("接入服务器"),
      subtitle: t("需要一台你自己的服务器，和一个你自己的域名"),
      wizard: true,
      render: render
    });
  }

  /* ---------------- 插件 · 命令行工具快捷键 ---------------- */

  var hotkeyState = null;
  var accessLogState = null;
  var portSitesState = null;

  var PLUGINS = [
    { id: "hotkeys", name: "命令行工具快捷键", sub: "常用命令速查与一键执行：frpc 重启、SSH 登录、实时查看日志" },
    { id: "port-sites", name: "本地端口管理", sub: "可以管理本地端口甚至通过隧道程序单独启动网站端口服务" },
    { id: "access-log", name: "访问日志", sub: "记录隧道访问来源 IP、路径、状态码与时间" }
  ];

  var COMBO_SYMBOL = { fn: "fn", ctrl: "⌃", opt: "⌥", shift: "⇧", cmd: "⌘" };
  var COMBO_KEY_LABEL = {
    space: "Space", "return": "↩", escape: "Esc", tab: "Tab",
    backspace: "⌫", "delete": "⌦", left: "←", right: "→", up: "↑", down: "↓"
  };
  var ACTION_LABEL = { run: "直接运行", terminal: "打开终端", iterm: "打开 iTerm" };
  var ACTION_HINT = {
    run: "在后台执行这条命令，并在屏幕右上角显示半透明运行窗口，方便查看输出和退出状态。",
    terminal: "打开系统自带「终端」；没有窗口时新建窗口，已有窗口时新建标签页并执行这条命令。",
    iterm: "打开 iTerm2 并粘贴执行这条命令（需要已安装 iTerm2）。"
  };

  function loadHotkeys() {
    return get("/api/plugins/hotkeys").then(function (s) { hotkeyState = s; return s; });
  }

  function currentPaletteCombo() {
    return (hotkeyState && hotkeyState.paletteCombo) || "fn+space";
  }

  // saveHotkeysConfig 整体替换配置并刷新本地缓存，不主动重绘视图（由调用方决定）。
  function saveHotkeysConfig(cfg) {
    if (!cfg.paletteCombo) cfg.paletteCombo = currentPaletteCombo();
    if (!cfg.orderVersion) cfg.orderVersion = (hotkeyState && hotkeyState.orderVersion) || 1;
    return put("/api/plugins/hotkeys", cfg).then(function () { return loadHotkeys(); });
  }

  function comboDisplay(combo) {
    if (!combo) return t("面板");
    return combo.split("+").map(function (p) {
      if (COMBO_SYMBOL[p]) return COMBO_SYMBOL[p];
      if (COMBO_KEY_LABEL[p]) return COMBO_KEY_LABEL[p];
      return p.toUpperCase();
    }).join("");
  }

  function hotkeyDisplayItems() {
    var items = (hotkeyState && hotkeyState.items) || [];
    return hotkeyState && hotkeyState.orderVersion > 0 ? items.slice() : items.slice().reverse();
  }

  function moveHotkeyItem(items, fromID, toID) {
    var next = items.filter(function (it) { return it.id !== fromID; });
    var moving = items.filter(function (it) { return it.id === fromID; })[0];
    if (!moving) return items;
    var idx = next.findIndex(function (it) { return it.id === toID; });
    if (idx < 0) return items;
    next.splice(idx, 0, moving);
    return next;
  }

  function newHotkeyID() {
    return "h" + Date.now().toString(36) + Math.random().toString(36).slice(2, 7);
  }

  function cloneHotkey(it) {
    return { id: it.id, name: it.name, combo: it.combo, action: it.action, command: it.command };
  }

  function renderPlugins() {
    var box = $("pluginsBody");
    clear(box);
    var card = el("div", "card");
    PLUGINS.forEach(function (p) {
      if (p.id === "hotkeys") card.appendChild(hotkeyPluginRow());
      else if (p.id === "access-log") card.appendChild(accessLogPluginRow());
      else if (p.id === "port-sites") card.appendChild(portSitesPluginRow());
    });
    box.appendChild(card);
    if (!hotkeyState) loadHotkeys().then(renderPlugins).catch(fail);
    if (!accessLogState) loadAccessLog().then(renderPlugins).catch(fail);
    if (!portSitesState) loadPortSites().then(renderPlugins).catch(fail);
  }

  function hotkeyPluginRow() {
    var row = el("div", "row");
    var main = el("div", "row-main");
    main.appendChild(el("h3", null, t("命令行工具快捷键")));
    main.appendChild(el("div", "row-sub", t("常用命令速查与一键执行：frpc 重启、SSH 登录、实时查看日志")));
    row.appendChild(main);

    var s = hotkeyState || { enabled: false, supported: true, items: [] };
    if (!s.supported) {
      row.appendChild(el("span", "port-chip", t("本系统不支持")));
    }

    var acts = el("div", "row-actions");
    var toggle = el(
      "button",
      "btn plugin-status-toggle " + (s.enabled ? "btn-primary" : "btn-danger-dashed"),
      s.enabled ? t("开启中") : t("关闭中")
    );
    toggle.type = "button";
    toggle.disabled = !s.supported;
    toggle.onclick = function () {
      toggle.disabled = true;
      var want = !s.enabled;
          saveHotkeysConfig({ enabled: want, items: hotkeyDisplayItems(), orderVersion: 1 })
        .then(function () {
          toast(want ? t("快捷键已开启") : t("快捷键已关闭"), "ok");
          renderPlugins();
        })
        .catch(function (e) { fail(e); toggle.disabled = false; });
    };

    var setBtn = el("button", "btn", t("设置"));
    setBtn.type = "button";
    setBtn.onclick = openHotkeySettings;
    acts.appendChild(setBtn);
    acts.appendChild(toggle);
    row.appendChild(acts);
    return row;
  }

  function loadAccessLog() {
    return get("/api/plugins/access-log").then(function (s) { accessLogState = s; return s; });
  }

  function loadPortSites() {
    return get("/api/plugins/port-sites").then(function (s) { portSitesState = s; return s; });
  }

  function portSitesPluginRow() {
    var row = el("div", "row");
    var main = el("div", "row-main");
    main.appendChild(el("h3", null, t("本地端口管理")));
    main.appendChild(el("div", "row-sub", t("可以管理本地端口甚至通过隧道程序单独启动网站端口服务")));
    row.appendChild(main);

    var s = portSitesState || { enabled: false, sites: [] };
    var acts = el("div", "row-actions");
    var toggle = el(
      "button",
      "btn plugin-status-toggle " + (s.enabled ? "btn-primary" : "btn-danger-dashed"),
      s.enabled ? t("开启中") : t("关闭中")
    );
    toggle.type = "button";
    toggle.onclick = function () {
      toggle.disabled = true;
      var want = !s.enabled;
      put("/api/plugins/port-sites", { enabled: want })
        .then(function (next) { portSitesState = next; return refresh(); })
        .then(function () {
          toast(want ? t("本地端口管理已开启") : t("本地端口管理已关闭"), "ok");
          renderPlugins();
        })
        .catch(function (e) { fail(e); toggle.disabled = false; });
    };

    var goBtn = el("button", "btn", t("去管理"));
    goBtn.type = "button";
    goBtn.onclick = function () { showView("portsites"); };
    acts.appendChild(goBtn);
    acts.appendChild(toggle);
    row.appendChild(acts);
    return row;
  }

  function syncPortSitesTab() {
    var tab = $("tabPortSites");
    if (!tab) return;
    var on = !!(state && state.portSites);
    tab.hidden = !on;
    if (!on && currentView === "portsites") showView("plugins");
  }

  function renderPortSites() {
    var list = $("portSiteList");
    if (!list) return;
    clear(list);
    var sites = (portSitesState && portSitesState.sites) || [];
    if (!sites.length) {
      list.appendChild(el("div", "placeholder", t("还没有本机网站。点右上角「新建站点」，例如开一个 5555 端口。")));
      return;
    }
    sites.forEach(function (site) {
      var card = el("div", "card portsite-card");
      var row = el("div", "row");
      row.appendChild(el("span", "dot " + (site.running ? "ok" : "off")));

      var main = el("div", "row-main");
      var a = el("a", "row-url", site.url);
      a.href = site.url;
      a.target = "_blank";
      a.rel = "noreferrer noopener";
      a.onclick = function (e) { e.stopPropagation(); };
      main.appendChild(a);
      main.appendChild(el("div", "row-sub",
        (site.running ? t("运行中") : t("已停止")) + "  ·  " + site.root));
      row.appendChild(main);

      var chip = el("a", "port-chip", ":" + site.port);
      chip.href = site.url;
      chip.target = "_blank";
      chip.rel = "noreferrer noopener";
      chip.onclick = function (e) { e.stopPropagation(); };
      row.appendChild(chip);

      var acts = el("div", "row-actions");
      var filesBtn = el("button", "btn btn-sm", t("文件管理"));
      filesBtn.type = "button";
      filesBtn.onclick = function (e) {
        e.stopPropagation();
        openPortSiteFiles(site);
      };
      var runBtn = el("button", "btn btn-sm " + (site.running ? "" : "btn-primary"), site.running ? t("停止") : t("启动"));
      runBtn.type = "button";
      runBtn.onclick = function (e) {
        e.stopPropagation();
        var path = "/api/plugins/port-sites/sites/" + site.port + "/" + (site.running ? "stop" : "start");
        post(path)
          .then(function (next) {
            portSitesState = next;
            toast(site.running ? t("已停止") : t("已启动"), "ok");
            renderPortSites();
          })
          .catch(fail);
      };
      var delBtn = el("button", "btn btn-sm btn-danger", t("删除"));
      delBtn.type = "button";
      delBtn.onclick = function (e) {
        e.stopPropagation();
        confirmDeletePortSite(site);
      };
      acts.appendChild(filesBtn);
      acts.appendChild(runBtn);
      acts.appendChild(delBtn);
      row.appendChild(acts);
      card.appendChild(row);
      card.onclick = function () { openPortSiteFiles(site); };
      list.appendChild(card);
    });
  }

  function openAddPortSite() {
    var portInput;
    var rootInput;
    var startBox;
    openModal({
      title: t("新建本机网站"),
      subtitle: t("在 127.0.0.1 上听一个端口，不自动建隧道"),
      body: function (box) {
        var f1 = el("div", "field");
        f1.appendChild(el("label", null, t("本机端口")));
        portInput = el("input", "input");
        portInput.type = "number";
        portInput.placeholder = t("例如 5555");
        portInput.min = "1";
        portInput.max = "65535";
        f1.appendChild(portInput);
        f1.appendChild(el("div", "hint", t("浏览器访问 http://127.0.0.1:端口。公网映射请到「隧道」页自己加。")));
        box.appendChild(f1);

        var f2 = el("div", "field");
        f2.appendChild(el("label", null, t("站点目录（可选）")));
        var dirRow = el("div", "field-inline");
        rootInput = el("input", "input");
        rootInput.placeholder = t("留空则用插件默认目录");
        dirRow.appendChild(rootInput);
        var pickBtn = el("button", "btn", t("选择"));
        pickBtn.type = "button";
        pickBtn.onclick = function () {
          pickBtn.disabled = true;
          post("/api/plugins/port-sites/pick-dir")
            .then(function (data) {
              if (data && data.canceled) return;
              if (data && data.path) rootInput.value = data.path;
            })
            .catch(fail)
            .then(function () { pickBtn.disabled = false; });
        };
        dirRow.appendChild(pickBtn);
        f2.appendChild(dirRow);
        f2.appendChild(el("div", "hint", t("默认写在 ~/.frp-ngrok/plugins/port-sites/{端口}/，也可以指定已有的第三方文件夹。")));
        box.appendChild(f2);

        var startLabel = el("label", "switch-inline");
        startBox = el("input");
        startBox.type = "checkbox";
        startBox.checked = true;
        startLabel.appendChild(startBox);
        startLabel.appendChild(el("span", null, t("创建后立即启动")));
        box.appendChild(startLabel);
      },
      confirmText: t("创建"),
      onConfirm: function (done) {
        var port = parseInt(portInput.value, 10);
        if (!port) {
          toast(t("请填写端口"), "bad");
          done();
          return;
        }
        post("/api/plugins/port-sites/sites", {
          port: port,
          root: (rootInput.value || "").trim(),
          start: !!startBox.checked
        })
          .then(function (next) {
            portSitesState = next;
            toast(t("站点已创建"), "ok");
            closeModal();
            renderPortSites();
          })
          .catch(function (e) { fail(e); done(); });
      }
    });
  }

  function confirmDeletePortSite(site) {
    var delFiles;
    openModal({
      title: t("删除站点"),
      subtitle: site.url,
      danger: true,
      confirmText: t("确认删除"),
      body: function (box) {
        box.appendChild(el("p", "muted", t("删除后这个本机网站会从列表消失，并立刻停止监听。")));
        var label = el("label", "switch-inline");
        delFiles = el("input");
        delFiles.type = "checkbox";
        delFiles.checked = !!site.deleteFilesDefault;
        label.appendChild(delFiles);
        label.appendChild(el("span", null, t("同时删除文件夹")));
        box.appendChild(label);
        box.appendChild(el("div", "hint",
          site.managed
            ? t("这是插件自己的默认目录，默认勾选删除。")
            : t("这是第三方目录，默认不删磁盘文件。")));
      },
      onConfirm: function (done) {
        var q = delFiles.checked ? "true" : "false";
        del("/api/plugins/port-sites/sites/" + site.port + "?deleteFiles=" + q)
          .then(function (next) {
            portSitesState = next;
            toast(t("已删除"), "ok");
            closeModal();
            renderPortSites();
          })
          .catch(function (e) { fail(e); done(); });
      }
    });
  }

  function portSiteDirQuery(dir) {
    return dir ? "?dir=" + encodeURIComponent(dir) : "";
  }

  function joinPortSiteDir(dir, name) {
    return dir ? dir + "/" + name : name;
  }

  function openPortSiteFiles(site) {
    var bodyBox;
    var footBox;
    var lastFiles = [];
    var confirming = null;
    var dir = "";
    var offset = 0;
    var limit = 100;
    var total = 0;

    function filesURL() {
      var q = portSiteDirQuery(dir);
      q += (q ? "&" : "?") + "offset=" + offset + "&limit=" + limit;
      return "/api/plugins/port-sites/sites/" + site.port + "/files" + q;
    }

    function applyList(data) {
      dir = data.dir || "";
      total = data.total || 0;
      offset = data.offset || 0;
      if (data.limit > 0) limit = data.limit;
      if (!(data.files || []).length && offset > 0) {
        offset = Math.max(0, offset - limit);
        load();
        return;
      }
      paint(data.files || []);
    }

    function load() {
      get(filesURL())
        .then(applyList)
        .catch(function (e) {
          clear(bodyBox);
          bodyBox.appendChild(el("div", "verdict bad", t(e.message)));
        });
    }

    function enterDir(next) {
      dir = next || "";
      offset = 0;
      load();
    }

    function paintCrumbs() {
      var nav = el("nav", "portsite-crumbs");
      nav.setAttribute("aria-label", t("当前目录"));
      if (!dir) {
        nav.appendChild(el("span", "portsite-crumb is-current", t("站点根")));
        return nav;
      }
      var rootBtn = el("button", "portsite-crumb", t("站点根"));
      rootBtn.type = "button";
      rootBtn.onclick = function () { enterDir(""); };
      nav.appendChild(rootBtn);
      var parts = dir.split("/");
      var acc = "";
      parts.forEach(function (part, i) {
        nav.appendChild(el("span", "portsite-crumb-sep", "/"));
        acc = acc ? acc + "/" + part : part;
        var here = acc;
        if (i === parts.length - 1) {
          nav.appendChild(el("span", "portsite-crumb is-current", part));
        } else {
          var b = el("button", "portsite-crumb", part);
          b.type = "button";
          b.onclick = function () { enterDir(here); };
          nav.appendChild(b);
        }
      });
      return nav;
    }

    function paintFooter() {
      if (!footBox) return;
      clear(footBox);
      if (confirming) {
        var cancel = el("button", "btn", t("取消"));
        cancel.type = "button";
        cancel.onclick = function () { confirming = null; paint(lastFiles); };
        footBox.appendChild(cancel);
        footBox.appendChild(el("div", "spacer"));
        var ok = el("button", "btn btn-danger", t("确认删除"));
        ok.type = "button";
        ok.onclick = function () {
          ok.disabled = true;
          del("/api/plugins/port-sites/sites/" + site.port + "/files/" + encodeURIComponent(confirming.name) + portSiteDirQuery(dir))
            .then(function (data) {
              toast(t("已删除"), "ok");
              confirming = null;
              applyList(data);
            })
            .catch(function (e) { fail(e); ok.disabled = false; });
        };
        footBox.appendChild(ok);
        return;
      }
      var left = el("button", "btn", t("打开文件夹"));
      left.type = "button";
      left.onclick = function () {
        post("/api/plugins/port-sites/sites/" + site.port + "/open" + portSiteDirQuery(dir))
          .then(function () { toast(t("已打开文件夹"), "ok"); })
          .catch(fail);
      };
      var close = el("button", "btn btn-primary", t("关闭"));
      close.type = "button";
      close.onclick = closeModal;
      footBox.appendChild(left);
      footBox.appendChild(el("div", "spacer"));
      footBox.appendChild(close);
    }

    function paint(files) {
      if (files) lastFiles = files;
      else files = lastFiles;
      clear(bodyBox);
      if (confirming) {
        var banner = el("div", "banner warn");
        var txt = el("div");
        txt.appendChild(el("strong", null, t("删除 ") + confirming.name));
        txt.appendChild(el("div", "muted", t("文件会从当前文件夹去掉，这里删了不能恢复。")));
        banner.appendChild(txt);
        bodyBox.appendChild(banner);
        paintFooter();
        return;
      }
      bodyBox.appendChild(paintCrumbs());

      var drop = el("div", "dropzone");
      drop.appendChild(el("div", null, dir ? t("把文件拖到这里，上传到当前文件夹") : t("把文件拖到这里，上传到站点根目录")));
      drop.ondragover = function (e) {
        e.preventDefault();
        drop.classList.add("is-over");
      };
      drop.ondragleave = function () { drop.classList.remove("is-over"); };
      drop.ondrop = function (e) {
        e.preventDefault();
        drop.classList.remove("is-over");
        var items = e.dataTransfer && e.dataTransfer.files;
        if (!items || !items.length) return;
        uploadPortSiteFiles(site.port, items, dir).then(function (data) {
          toast(t("已上传"), "ok");
          applyList(data);
        }).catch(fail);
      };
      bodyBox.appendChild(drop);

      if (!files.length) {
        bodyBox.appendChild(el("div", "placeholder", t("这个目录还是空的。")));
      } else {
        var card = el("div", "card portsite-file-list");
        files.forEach(function (f) {
          var row = el("div", "row portsite-file-row" + (f.isDir ? " is-dir" : ""));
          var name = el("div", "row-url", f.name);
          row.appendChild(name);
          row.appendChild(el("span", "portsite-file-size", f.isDir ? t("文件夹") : (f.size + " B")));
          var delBtn = el("button", "btn btn-sm btn-danger", t("删除"));
          delBtn.type = "button";
          delBtn.onclick = function (e) {
            e.stopPropagation();
            if (f.isDir) {
              toast(t("文件夹请到「打开文件夹」里处理"));
              return;
            }
            confirming = f;
            paint(lastFiles);
          };
          row.appendChild(delBtn);
          if (f.isDir) {
            row.onclick = function () { enterDir(joinPortSiteDir(dir, f.name)); };
          }
          card.appendChild(row);
        });
        bodyBox.appendChild(card);
      }

      if (limit > 0 && total > limit) {
        var pager = el("div", "portsite-file-pager");
        var from = offset + 1;
        var to = offset + files.length;
        pager.appendChild(el("span", "muted", from + "–" + to + " / " + total));
        var acts = el("div", "portsite-file-pager-acts");
        var prev = el("button", "btn btn-sm", t("上一页"));
        prev.type = "button";
        prev.disabled = offset <= 0;
        prev.onclick = function () {
          offset = Math.max(0, offset - limit);
          load();
        };
        var next = el("button", "btn btn-sm", t("下一页"));
        next.type = "button";
        next.disabled = offset + files.length >= total;
        next.onclick = function () {
          offset += limit;
          load();
        };
        acts.appendChild(prev);
        acts.appendChild(next);
        pager.appendChild(acts);
        bodyBox.appendChild(pager);
      }
      paintFooter();
    }

    // 弹窗只有一层：删除确认切到本窗确认态，不用浏览器 confirm（Chrome 会把 127.0.0.1 塞进标题，很难看）。
    openModal({
      title: t("站点文件"),
      subtitle: site.url + "  ·  " + site.root,
      wizard: true,
      render: function (body, foot, progress) {
        progress.style.display = "none";
        bodyBox = body;
        footBox = foot;
        body.appendChild(el("div", "muted", t("加载中…")));
        paintFooter();
        load();
      }
    });
  }

  function uploadPortSiteFiles(port, fileList, dir) {
    var chain = Promise.resolve({ files: [] });
    Array.prototype.forEach.call(fileList, function (file) {
      chain = chain.then(function () {
        var fd = new FormData();
        fd.append("file", file, file.name);
        return fetch("/api/plugins/port-sites/sites/" + port + "/files" + portSiteDirQuery(dir), {
          method: "POST",
          headers: { Authorization: "Bearer " + token },
          body: fd
        }).then(function (res) {
          return res.json().catch(function () { return {}; }).then(function (data) {
            if (!res.ok) throw new Error(data.error ? t(data.error) : t("上传失败"));
            return data;
          });
        });
      });
    });
    return chain;
  }

  function accessLogPluginRow() {
    var row = el("div", "row");
    var main = el("div", "row-main");
    main.appendChild(el("h3", null, t("访问日志")));
    main.appendChild(el("div", "row-sub", t("记录隧道访问来源 IP、路径、状态码与时间")));
    row.appendChild(main);

    var s = accessLogState || { enabled: false, tunnels: [] };
    var acts = el("div", "row-actions");
    var toggle = el(
      "button",
      "btn plugin-status-toggle " + (s.enabled ? "btn-primary" : "btn-danger-dashed"),
      s.enabled ? t("开启中") : t("关闭中")
    );
    toggle.type = "button";
    toggle.onclick = function () {
      toggle.disabled = true;
      var want = !s.enabled;
      put("/api/plugins/access-log", { enabled: want })
        .then(function () { return loadAccessLog(); })
        .then(function () { return refresh(); })
        .then(function () {
          toast(want ? t("访问日志已开启") : t("访问日志已关闭"), "ok");
          renderPlugins();
        })
        .catch(function (e) { fail(e); toggle.disabled = false; });
    };

    var setBtn = el("button", "btn", t("设置"));
    setBtn.type = "button";
    setBtn.onclick = openAccessLogSettings;
    acts.appendChild(setBtn);
    acts.appendChild(toggle);
    row.appendChild(acts);
    return row;
  }

  function openAccessLogSettings() {
    if (!accessLogState) {
      loadAccessLog().then(openAccessLogSettings).catch(fail);
      return;
    }
    var bodyBox;

    function paint() {
      clear(bodyBox);
      var list = accessLogState.tunnels || [];
      if (!list.length) {
        bodyBox.appendChild(el("div", "placeholder", t("还没有隧道。添加隧道后会出现在这里，默认开启记录。")));
        return;
      }
      var card = el("div", "card");
      list.forEach(function (tn) {
        var row = el("div", "row");
        var main = el("div", "row-main");
        main.appendChild(el("div", "row-url", tn.host || tn.url));
        main.appendChild(el("div", "row-sub", t("本机 :") + tn.localPort + t("  ·  日志 ") + tn.sizeText));
        row.appendChild(main);
        var acts = el("div", "row-actions");
        var sw = el(
          "button",
          "btn btn-sm plugin-status-toggle " + (tn.logging ? "btn-primary" : "btn-danger-dashed"),
          tn.logging ? t("记录中") : t("已暂停")
        );
        sw.type = "button";
        sw.onclick = function () {
          sw.disabled = true;
          put("/api/plugins/access-log/tunnels/" + tn.localPort, { enabled: !tn.logging })
            .then(function () { return loadAccessLog(); })
            .then(function () {
              toast(tn.logging ? t("已暂停这条隧道的访问日志") : t("已开启这条隧道的访问日志"), "ok");
              paint();
            })
            .catch(function (e) { fail(e); sw.disabled = false; });
        };
        acts.appendChild(sw);
        var delBtn = el("button", "btn btn-sm btn-danger", t("删除"));
        delBtn.type = "button";
        delBtn.disabled = !tn.size;
        delBtn.onclick = function () {
          delBtn.disabled = true;
          del("/api/plugins/access-log/tunnels/" + tn.localPort + "/log")
            .then(function () { return loadAccessLog(); })
            .then(function () {
              toast(t("已删除 ") + tn.host + t(" 的访问日志"), "ok");
              paint();
            })
            .catch(function (e) { fail(e); delBtn.disabled = false; });
        };
        acts.appendChild(delBtn);
        row.appendChild(acts);
        card.appendChild(row);
      });
      bodyBox.appendChild(card);
      bodyBox.appendChild(el("div", "hint", t("总开关关闭后不再记录，隧道列表和日志页的入口也会收起。单条暂停只停这一条。")));
    }

    openModal({
      title: t("访问日志"),
      subtitle: t("按隧道开关记录，并查看每条日志占用的空间"),
      hideCancel: true,
      confirmText: t("关闭"),
      body: function (box) { bodyBox = box; paint(); },
      onConfirm: function () { closeModal(); }
    });
  }

  /* openHotkeySettings 弹出一个列表+表单的弹窗：列表里试跑/编辑/删除，表单里新增或改。
     因为弹窗系统只有一层，删除确认直接在本弹窗内切换成「确认态」，不再套第二层。 */
  function openHotkeySettings() {
    if (!hotkeyState) {
      loadHotkeys().then(openHotkeySettings).catch(fail);
      return;
    }
    var bodyBox, footBox;
    var editing = null;    // 非空 = 表单态，值为正在编辑的条目副本
    var editingNew = false;
    var confirming = null; // 非空 = 删除确认态，值为待删条目
    var dragID = "";

    function paint() {
      clear(bodyBox);
      clear(footBox);
      if (confirming) paintConfirm(bodyBox, footBox);
      else if (editing) paintForm(bodyBox, footBox);
      else paintList(bodyBox, footBox);
    }

    function paintList(body, foot) {
      var items = hotkeyDisplayItems();
      if (!items.length) {
        body.appendChild(el("div", "placeholder", t("还没有快捷键。点右下角「添加快捷键」配一条，之后随时按下即可执行。")));
      } else {
        var card = el("div", "card hotkey-list");
        items.forEach(function (it) {
          var row = el("div", "row hotkey-row");
          row.draggable = true;
          row.dataset.id = it.id;
          row.ondragstart = function (e) {
            dragID = it.id;
            row.classList.add("is-dragging");
            if (e.dataTransfer) {
              e.dataTransfer.effectAllowed = "move";
              e.dataTransfer.setData("text/plain", it.id);
            }
          };
          row.ondragend = function () {
            dragID = "";
            row.classList.remove("is-dragging");
          };
          row.ondragover = function (e) {
            e.preventDefault();
            if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
          };
          row.ondrop = function (e) {
            e.preventDefault();
            if (!dragID || dragID === it.id) return;
            var ordered = moveHotkeyItem(hotkeyDisplayItems(), dragID, it.id);
            saveHotkeysOrder(ordered);
          };
          row.appendChild(el("span", "port-chip", comboDisplay(it.combo)));
          var main = el("div", "row-main hotkey-main");
          main.appendChild(el("div", "row-url", it.name));
          main.appendChild(el("div", "row-sub hotkey-meta", t(ACTION_LABEL[it.action]) + "  ·  " + it.command));
          row.appendChild(main);

          var acts = el("div", "row-actions hotkey-actions");
          var runBtn = el("button", "btn btn-sm", t("试跑"));
          runBtn.type = "button";
          runBtn.title = t("现在执行一次这条命令，验证写得对不对");
          runBtn.onclick = function () { testHotkeyItem(it, runBtn); };
          acts.appendChild(runBtn);
          var editBtn = el("button", "btn btn-sm", t("编辑"));
          editBtn.type = "button";
          editBtn.onclick = function () { editing = cloneHotkey(it); editingNew = false; paint(); };
          acts.appendChild(editBtn);
          var delBtn = el("button", "btn btn-sm btn-danger", t("删除"));
          delBtn.type = "button";
          delBtn.onclick = function () { confirming = it; paint(); };
          acts.appendChild(delBtn);
          row.appendChild(acts);
          card.appendChild(row);
        });
        body.appendChild(card);
        body.appendChild(el("div", "hint", t("开启状态下这些快捷键全局生效，浏览器关着也能用。")));
      }

      var close = el("button", "btn", t("关闭"));
      close.type = "button";
      close.onclick = closeModal;
      foot.appendChild(close);
      foot.appendChild(el("div", "spacer"));
      var add = el("button", "btn btn-primary", t("+ 添加快捷键"));
      add.type = "button";
      add.onclick = function () {
        editing = { id: "", name: "", combo: "", action: "run", command: "" };
        editingNew = true;
        paint();
      };
      foot.appendChild(add);
    }

    function saveHotkeysOrder(ordered) {
      saveHotkeysConfig({ enabled: !!hotkeyState.enabled, items: ordered, orderVersion: 1 })
        .then(function () { toast(t("顺序已保存"), "ok"); paint(); })
        .catch(fail);
    }

    function paintConfirm(body, foot) {
      var banner = el("div", "banner warn");
      var txt = el("div");
      txt.appendChild(el("strong", null, t("删除 ") + confirming.name));
      txt.appendChild(el("div", "muted", t("组合键 ") + comboDisplay(confirming.combo) + t(" 会立即失效。")));
      banner.appendChild(txt);
      body.appendChild(banner);

      var cancel = el("button", "btn", t("取消"));
      cancel.type = "button";
      cancel.onclick = function () { confirming = null; paint(); };
      foot.appendChild(cancel);
      foot.appendChild(el("div", "spacer"));
      var del = el("button", "btn btn-danger", t("确认删除"));
      del.type = "button";
      del.onclick = function () {
        del.disabled = true;
        var items = hotkeyDisplayItems().filter(function (x) { return x.id !== confirming.id; });
        saveHotkeysConfig({ enabled: !!hotkeyState.enabled, items: items, orderVersion: 1 })
          .then(function () { toast(t("已删除"), "ok"); confirming = null; paint(); })
          .catch(function (e) { fail(e); del.disabled = false; });
      };
      foot.appendChild(del);
    }

    function paintForm(body, foot) {
      var nameField = el("div", "field");
      nameField.appendChild(el("label", null, t("名称")));
      var nameInput = el("input", "input");
      nameInput.placeholder = t("例如 重启 frpc / SSH 登录");
      nameInput.value = editing.name || "";
      nameInput.oninput = function () { editing.name = nameInput.value; };
      nameField.appendChild(nameInput);
      body.appendChild(nameField);

      var comboField = el("div", "field");
      comboField.appendChild(el("label", null, t("组合键（可选）")));
      var comboBtn = el("button", "btn", editing.combo ? comboDisplay(editing.combo) : t("不设置，作为面板命令"));
      comboBtn.type = "button";
      comboBtn.style.fontFamily = "var(--font-mono)";
      comboBtn.onclick = function () {
        comboBtn.textContent = t("正在监听，请按下组合键…");
        captureHotkeyCombo(
          function (combo) { editing.combo = combo; paint(); },
          function () { paint(); }
        );
      };
      comboField.appendChild(comboBtn);
      comboField.appendChild(el("div", "hint", t("可不设置组合键，只通过命令面板搜索执行。要设置时，按住 fn / ⌘ / ⌃ / ⌥ / ⇧ 再按一个键；裸 Esc 取消。")));
      body.appendChild(comboField);

      var actionField = el("div", "field");
      actionField.appendChild(el("label", null, t("执行方式")));
      var seg = el("div", "seg");
      [
        { id: "run", label: t("直接运行") },
        { id: "terminal", label: t("打开终端") },
        { id: "iterm", label: t("打开 iTerm") }
      ].forEach(function (o) {
        var b = el("button", null, o.label);
        b.type = "button";
        b.classList.toggle("is-active", o.id === editing.action);
        b.onclick = function () { editing.action = o.id; paint(); };
        seg.appendChild(b);
      });
      actionField.appendChild(seg);
      body.appendChild(actionField);

      var cmdField = el("div", "field");
      cmdField.appendChild(el("label", null, t("命令")));
      var cmdInput = el("input", "input");
      cmdInput.value = editing.command || "";
      cmdInput.placeholder = t("例如 ssh root@1.2.3.4");
      cmdInput.oninput = function () { editing.command = cmdInput.value; };
      cmdField.appendChild(cmdInput);
      cmdField.appendChild(el("div", "hint", t(ACTION_HINT[editing.action])));
      body.appendChild(cmdField);

      var back = el("button", "btn", t("返回"));
      back.type = "button";
      back.onclick = function () { editing = null; paint(); };
      foot.appendChild(back);
      foot.appendChild(el("div", "spacer"));
      var save = el("button", "btn btn-primary", editingNew ? t("添加") : t("保存"));
      save.type = "button";
      save.onclick = function () {
        var name = nameInput.value.trim();
        var command = cmdInput.value.trim();
        if (!name) { toast(t("请给这条快捷键起个名字"), "bad"); return; }
        if (!command) { toast(t("请填写要执行的命令"), "bad"); return; }
        var item = {
          id: editing.id || newHotkeyID(),
          name: name, combo: editing.combo || "", action: editing.action, command: command
        };
        var items = hotkeyDisplayItems().filter(function (x) { return x.id !== item.id; });
        if (editingNew) {
          items.unshift(item);
        } else {
          var replaced = false;
          items = hotkeyDisplayItems().map(function (x) {
            if (x.id !== item.id) return x;
            replaced = true;
            return item;
          });
          if (!replaced) items.unshift(item);
        }
        save.disabled = true;
        saveHotkeysConfig({ enabled: !!hotkeyState.enabled, items: items, orderVersion: 1 })
          .then(function () {
            toast(t("快捷键已保存"), "ok");
            editing = null;
            paint();
          })
          .catch(function (e) { fail(e); save.disabled = false; });
      };
      foot.appendChild(save);
    }

      openModal({
        title: t("命令行工具快捷键"),
        subtitle: comboDisplay(currentPaletteCombo()) + t(" · 按下后在屏幕中央搜索并执行快捷命令；总开关关闭时不生效。"),
      wizard: true,
      render: function (body, foot, progress) {
        progress.style.display = "none";
        bodyBox = body;
        footBox = foot;
        paint();
      }
    });
  }

  function testHotkeyItem(it, btn) {
    btn.disabled = true;
    var origin = btn.textContent;
    btn.textContent = t("运行中…");
    post("/api/plugins/hotkeys/run", { id: it.id, name: it.name, action: it.action, command: it.command })
      .then(function (r) {
        toast(r.ok ? (t(r.message) || t("已执行")) : (t(r.message) || t("执行失败")), r.ok ? "ok" : "bad");
      })
      .catch(fail)
      .finally(function () {
        btn.disabled = false;
        btn.textContent = origin;
      });
  }

  // normalizeComboKey 把浏览器的 KeyboardEvent.key 归一到后端认识的按键名。
  function normalizeComboKey(k) {
    if (!k) return "";
    if (/^[a-z]$/i.test(k)) return k.toLowerCase();
    if (/^[0-9]$/.test(k)) return k;
    if (/^F([1-9]|1[0-2])$/i.test(k)) return k.toLowerCase();
    var map = {
      " ": "space", Enter: "return", Escape: "escape", Tab: "tab",
      Backspace: "backspace", Delete: "delete",
      ArrowLeft: "left", ArrowRight: "right", ArrowUp: "up", ArrowDown: "down"
    };
    return map[k] || "";
  }

  // captureHotkeyCombo 监听下一次按键组合，命中即回调 onDone，裸 Esc 回调 onCancel。
  // 监听挂在弹窗容器上：弹窗一关，监听自然失效，不会漏到后续按键。
  function captureHotkeyCombo(onDone, onCancel) {
    var node = $("modalBox");
    var finished = false;
    function handler(e) {
      e.preventDefault();
      e.stopPropagation();
      var key = normalizeComboKey(e.key);
      var mods = [];
      if (e.getModifierState && e.getModifierState("Fn")) mods.push("fn");
      if (e.ctrlKey) mods.push("ctrl");
      if (e.altKey) mods.push("opt");
      if (e.shiftKey) mods.push("shift");
      if (e.metaKey) mods.push("cmd");
      if (!mods.length) {
        if (key === "escape") {
          finished = true;
          node.removeEventListener("keydown", handler, true);
          onCancel();
        }
        return;
      }
      if (!key) return; // 不支持的键，继续等
      finished = true;
      node.removeEventListener("keydown", handler, true);
      onDone(mods.concat([key]).join("+"));
    }
    node.addEventListener("keydown", handler, true);
  }

  /* ---------------- 弹窗 ---------------- */

  function openModal(opt) {
    var root = $("modalRoot");
    var box = $("modalBox");
    clear(box);
    root.hidden = false;

    var head = el("div", "modal-head");
    head.appendChild(el("h2", null, opt.title));
    if (opt.subtitle) head.appendChild(el("p", null, opt.subtitle));
    box.appendChild(head);

    var progress = el("div", "wizard-progress");
    if (opt.wizard) box.appendChild(progress);

    var body = el("div", "modal-body");
    box.appendChild(body);

    var foot = el("div", "modal-foot");
    box.appendChild(foot);

    if (opt.wizard) {
      opt.render(body, foot, progress);
      return;
    }

    // 只读弹窗没有「取消」的说法，一颗关闭按钮就够
    if (opt.leftText) {
      var left = el("button", "btn", opt.leftText);
      left.type = "button";
      left.onclick = function () {
        if (opt.onLeft) opt.onLeft();
      };
      foot.appendChild(left);
    }
    if (!opt.hideCancel) {
      var cancel = el("button", "btn", t("取消"));
      cancel.onclick = closeModal;
      foot.appendChild(cancel);
    }
    var confirm = el("button",
      "btn modal-confirm " + (opt.danger ? "btn-danger" : "btn-primary"), opt.confirmText || t("确认"));
    confirm.onclick = function () {
      confirm.disabled = true;
      opt.onConfirm(function () { confirm.disabled = false; });
    };
    foot.appendChild(el("div", "spacer"));
    foot.appendChild(confirm);
    // 按钮先挂好再渲染正文：正文里的联动要按当前选项改写确认按钮的文案，
    // 反过来的话首次渲染时按钮还不存在，得靠 setTimeout 补一刀才对得上。
    opt.body(body);
  }

  function closeModal() {
    $("modalRoot").hidden = true;
    clear($("modalBox"));
  }

  /* ---------------- 视图切换 ---------------- */

  function showView(name) {
    // 初次没有档案时 currentView 会进入 empty；档案创建成功后必须把这个临时
    // 状态收回到真正的默认页，否则顶部已经有服务器，正文却永远还是接入卡片。
    if (state && state.current && name === "empty") name = "tunnels";
    if (name === "portsites" && !(state && state.portSites)) name = "plugins";
    currentView = name;
    ["empty", "tunnels", "check", "deploy", "logs", "settings", "plugins", "portsites"].forEach(function (v) {
      $("view-" + v).hidden = true;
    });

    if (!state || !state.current) {
      $("view-empty").hidden = false;
      $("tabs").style.display = "none";
      return;
    }
    $("tabs").style.display = "";
    $("view-" + name).hidden = false;

    Array.prototype.forEach.call(document.querySelectorAll(".tab"), function (tab) {
      tab.classList.toggle("is-active", tab.dataset.nav === name);
    });

    if (name === "check") {
      if (!paintCachedCheck() && !$("checkResult").firstChild) runCheck();
    }
    if (name === "tunnels") renderTunnels();
    if (name === "deploy") renderDeploy();
    if (name === "settings") renderSettings();
    if (name === "plugins") renderPlugins();
    if (name === "portsites") {
      if (!portSitesState) loadPortSites().then(renderPortSites).catch(fail);
      else renderPortSites();
    }
    if (name === "logs") { loadLog(); }
    syncPortSitesTab();
    syncLogTimer();
  }

  /* ---------------- 数据刷新 ---------------- */

  function refresh() {
    return get("/api/state").then(function (s) {
      applyStateLocale(s);
      state = s;
      renderTop();
      showView(state.current ? currentView : "empty");
      syncPortSitesTab();
    });
  }

  function boot() {
    refresh()
      .then(function () {
        $("boot").hidden = true;
        $("app").hidden = false;
        // 轮询保持状态新鲜：连接状态可能被后台重连改变
        pollTimer = setInterval(function () {
          if (document.hidden) return;
          get("/api/state").then(onPolledState).catch(function () { /* 轮询失败静默处理 */ });
        }, 4000);
      })
      .catch(function (e) {
        $("boot").innerHTML =
          t('<p style="text-align:center;line-height:2">连接本地服务失败<br>') +
          '<span class="muted">' + (e.message || "") + '</span><br><br>' +
          t('<span class="muted">请重新双击「frp-ngrok」启动器打开页面。</span></p>');
      });
  }

  /* ---------------- 事件绑定 ---------------- */

  document.addEventListener("click", function (e) {
    var locBtn = e.target.closest("[data-locale]");
    if (locBtn) {
      var id = locBtn.getAttribute("data-locale");
      if (!id || id === currentLocale()) return;
      applyLocale(id).catch(fail);
      return;
    }
    var nav = e.target.closest("[data-nav]");
    if (nav) return showView(nav.dataset.nav);
    if (e.target.dataset && e.target.dataset.close) closeModal();
  });

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !$("modalRoot").hidden) closeModal();
  });

  $("btnWizardStart").onclick = startWizard;
  $("btnAddTunnel").onclick = openAddTunnel;
  $("btnAddPortSite").onclick = openAddPortSite;
  $("btnRunCheck").onclick = runCheck;
  $("btnCopyScript").onclick = copyDeployScript;
  $("btnRefreshLog").onclick = loadLog;
  $("logAuto").onchange = syncLogTimer;
  $("logKinds").onclick = function (e) {
    var btn = e.target.closest("[data-log-kind]");
    if (!btn || !state || !state.accessLog) return;
    logKind = btn.dataset.logKind;
    loadLog();
    syncLogTimer();
  };

  boot();
})();
