#!/usr/bin/env bash
#
# 用无头浏览器渲染控制台页面并截图，便于在不启动后台服务的情况下核对视觉效果。
#
#   ./scripts/preview.sh                        渲染隧道页
#   ./scripts/preview.sh settings               渲染指定页签
#   ./scripts/preview.sh deploy single          按单域名模式渲染
#   PREVIEW_RENDER_ONLY=1 ./scripts/preview.sh tunnels created-single
#       首次无档案、下一轮刷新出现单域名档案，用来回归空状态转场
#   ./scripts/preview.sh tunnels single-empty 0 add-tunnel
#       单域名底座还没挂隧道时打开新增弹窗
#   PREVIEW_RENDER_ONLY=1 ./scripts/preview.sh tunnels empty 0 wizard-single
#       无档案时打开接入向导并选择单域名，用来回归域名与端口同行
#   ./scripts/preview.sh tunnels none           按无底座渲染（底座已被删掉）
#   ./scripts/preview.sh tunnels wildcard-empty 泛域名底座还在，但底下没有隧道
#   ./scripts/preview.sh deploy wildcard 420    渲染后滚动 420px，用来看吸顶栏遮挡
#   ./scripts/preview.sh tunnels wildcard 0 add-tunnel
#       渲染后自动打开弹窗，可选 add-tunnel / add-tunnel-domain / server / domains
#
set -euo pipefail

cd "$(dirname "$0")/.."

VIEW="${1:-tunnels}"
MODE="${2:-wildcard}"
SCROLL="${3:-0}"
MODAL="${4:-}"
OUT_DIR="build/preview"
CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
CACHE_BUST="$(date +%s)"

if [ ! -x "$CHROME" ]; then
    echo "找不到 Chrome，无法生成预览"
    exit 1
fi

mkdir -p "$OUT_DIR"
cp web/dist/style.css web/dist/app.js web/dist/i18n.js "$OUT_DIR/"

# 用假数据顶替后端接口，页面逻辑与线上完全一致
cat > "$OUT_DIR/mock.js" <<'MOCK'
(function () {
  var MODE = "__MODE__";
  // wildcard-empty 是「底座还在但底下一条隧道都没有」，删除按钮只在这时出现
  var empty = MODE === "wildcard-empty";
  var singleEmpty = MODE === "single-empty" || MODE === "created-single";
  var createdSingle = MODE === "created-single";
  var noProfile = MODE === "empty";
  var none = MODE === "none";
  var mode = empty ? "wildcard" : ((singleEmpty || noProfile) ? "single" : MODE);
  var wild = !none && mode !== "single";
  var IP = "203.0.113.10";
  var DOMAIN = none ? "" : (wild ? "cpolar.mysite.com" : "www.mysite.com");

  var profile = {
    name: "demo", domain: DOMAIN, domainMode: mode, serverIp: IP,
    serverPort: 7000, vhostPort: 18080, current: true
  };

  // 独立域名那条不挂在档案域名下，两种绑定方式要能在同一屏里对照着看
  var owned = {
    name: "sitewww-shop-com", localPort: 8080, subdomain: "",
    customDomain: "www.shop.com", host: "www.shop.com",
    url: "https://www.shop.com/", localUp: true
  };

  // 无底座：底座被删掉之后，能留下的只剩各自绑了独立域名的隧道
  var tunnels = singleEmpty
    ? []
    : (none || empty)
    ? [owned]
    : wild
    ? [
        { name: "local9999", localPort: 9999, subdomain: "9999", customDomain: "",
          host: "9999." + DOMAIN, url: "https://9999." + DOMAIN + "/", localUp: true },
        { name: "local9527", localPort: 9527, subdomain: "9527", customDomain: "",
          host: "9527." + DOMAIN, url: "https://9527." + DOMAIN + "/", localUp: true },
        { name: "local3000", localPort: 3000, subdomain: "web", customDomain: "",
          host: "web." + DOMAIN, url: "https://web." + DOMAIN + "/", localUp: false },
        owned
      ]
    : [
        { name: "local3000", localPort: 3000, subdomain: "", customDomain: "",
          host: DOMAIN, url: "https://" + DOMAIN + "/", localUp: true },
        owned
      ];

  var state = {
    version: "1.0.0",
    port: 17890,
    autostart: true,
    frpcVersion: "0.70.1",
    locale: "en",
    dataDir: "~/.frp-ngrok/frp",
    profiles: [profile],
    current: profile,
    tunnels: tunnels,
    client: createdSingle
      ? { state: "login_failed", profile: "demo", pid: 12345,
          since: new Date().toISOString(), restarts: 0, lastError: "测试：密钥不一致" }
      : { state: "running", profile: "demo", pid: 12345,
          since: new Date().toISOString(), restarts: 0, lastError: "" }
  };

  var emptyState = {
    version: "1.0.0", port: 17890, autostart: true, frpcVersion: "0.70.1",
    locale: "en",
    dataDir: "~/.frp-ngrok/frp", profiles: [], current: null, tunnels: [],
    client: { state: "stopped", profile: "", pid: 0, since: "", restarts: 0, lastError: "" }
  };
  var stateReads = 0;

  var plan = {
    script: "#!/usr/bin/env bash\n# frps 服务端一键部署（" + (wild ? "泛域名" : "单域名") + " vhost 模式）\nset -euo pipefail\n\nFRP_VERSION=\"0.70.1\"\nFRP_DIR=\"/www/frp\"\nBIND_PORT=7000\nVHOST_PORT=18080\nPUBLIC_DOMAIN=\"" + DOMAIN + "\"\n...",
    nginxConfig: "location / {\n    proxy_pass http://127.0.0.1:18080;\n\n    # frps 靠 Host 头选择隧道，不能删\n    proxy_set_header Host $host;\n    proxy_set_header X-Real-IP $remote_addr;\n    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n    proxy_set_header X-Forwarded-Proto $scheme;\n\n    # WebSocket\n    proxy_http_version 1.1;\n    proxy_set_header Upgrade $http_upgrade;\n    proxy_set_header Connection \"upgrade\";\n\n    # 长任务与大请求\n    proxy_connect_timeout 60s;\n    proxy_send_timeout 300s;\n    proxy_read_timeout 300s;\n    client_max_body_size 100m;\n\n    # SSE / 流式输出\n    proxy_buffering off;\n}",
    path: "~/.frp-ngrok/frp/server/deploy-frps-demo.sh",
    domain: DOMAIN, domainMode: MODE, rootDomain: "mysite.com",
    ip: IP, port: 7000, vhost: 18080, token: "preview-token",
    dnsRecords: none
      ? []
      : wild
      ? [{ host: "cpolar", type: "A", value: IP, fqdn: DOMAIN },
         { host: "*.cpolar", type: "A", value: IP, fqdn: "*." + DOMAIN }]
      : [{ host: "www", type: "A", value: IP, fqdn: DOMAIN }],
    siteDomains: none ? [] : (wild ? [DOMAIN, "*." + DOMAIN] : [DOMAIN]),
    certNote: none
      ? "这台服务器没有底座域名：站点与证书跟着各条隧道的独立域名走，每个域名各建一个站点、各签一张普通证书即可。"
      : wild
      ? "证书用 Let's Encrypt 并选 DNS 验证——通配符证书只能用这种方式签发。签好后打开强制 HTTPS。"
      : "证书用 Let's Encrypt 即可，单域名走默认的 HTTP 验证就能签。签好后打开强制 HTTPS。"
  };

  var routes = {
    "/api/state": noProfile ? emptyState : state,
    "/api/logs": { log: "2026-08-12 13:55:01 [I] [service.go:305] login to server success\n2026-08-12 13:55:01 [I] [proxy_manager.go:173] proxy added: [local9999 local9527]\n2026-08-12 13:55:02 [I] [control.go:169] [9527] start proxy success", path: "~/.frp-ngrok/frp/profiles/demo/frpc.log" },
    "/api/deploy-script": plan,
    "/api/profiles": { profile: profile, script: plan.script, activated: true }
  };

  window.__previewRequests = [];
  window.fetch = function (path, options) {
    var key = String(path).split("?")[0];
    window.__previewRequests.push({
      path: key,
      method: options && options.method ? options.method : "GET",
      body: options && options.body ? JSON.parse(options.body) : null
    });
    if (key === "/api/prefs" && options && options.body) {
      var pref = JSON.parse(options.body);
      if (pref.locale) {
        state.locale = pref.locale;
        emptyState.locale = pref.locale;
      }
    }
    if (key === "/api/profiles" && options && options.body) {
      document.documentElement.dataset.previewProfileRequest = options.body;
    }
    // 部署页按档案 ID 取方案，路径里带着档案名，只能按后缀认
    var body = /\/deploy-plan$/.test(key) ? plan : (routes[key] || { ok: true });
    if (key === "/api/state" && createdSingle && stateReads++ === 0) body = emptyState;
    return Promise.resolve({
      ok: true,
      status: 200,
      json: function () { return Promise.resolve(body); }
    });
  };

  try { localStorage.setItem("frp-ngrok.token", "preview"); } catch (e) { /* 忽略 */ }

  // 渲染完成后切到目标页签，再按需滚动（用来核对吸顶栏会不会被正文穿透）
  setTimeout(function () {
    var tab = document.querySelector('[data-nav="__VIEW__"]');
    if (tab && !createdSingle) tab.click();

    // 部署页进去就会自动拉当前服务器的方案，不用再替用户填什么
    setTimeout(function () {
      window.scrollTo(0, __SCROLL__);
      openModal("__MODAL__");
    }, 700);
  }, 200);

  // openModal 替用户点开某个弹窗，好把它一起截进来
  function openModal(which) {
    if (!which) return;
    if (which === "wizard-single") {
      var start = document.getElementById("btnWizardStart");
      if (start) start.click();
      var modes = document.querySelectorAll("#modalBox .seg button");
      if (modes.length) modes[0].click();
      return;
    }
    // 删底座那颗按钮藏在域名弹窗里，得先把域名弹窗点开再点它
    if (which === "delete-base") {
      openModal("domains");
      var del = document.querySelector("#modalBox .row-actions .btn-danger");
      if (del) del.click();
      return;
    }
    if (which.indexOf("add-tunnel") === 0) {
      var add = document.getElementById("btnAddTunnel");
      if (add) add.click();
      if (which === "add-tunnel-domain") {
        var seg = document.querySelectorAll("#modalBox .seg button");
        if (seg.length > 1) seg[1].click();
      }
      return;
    }
    // 概览卡片上的两颗按钮：第一颗编辑服务器，第二颗看域名
    var btns = document.querySelectorAll("#statsGrid .stat-head button");
    var idx = which === "server" ? 0 : 1;
    if (btns[idx]) btns[idx].click();

  }
})();
MOCK

sed -i '' -e "s|__VIEW__|$VIEW|" -e "s|__MODE__|$MODE|" -e "s|__SCROLL__|$SCROLL|" \
    -e "s|__MODAL__|$MODAL|" "$OUT_DIR/mock.js"

# 注入假数据脚本，并把绝对路径改成相对路径
sed -e "s|<script src=\"/i18n.js\"></script>|<script src=\"mock.js?v=$CACHE_BUST\"></script><script src=\"i18n.js?v=$CACHE_BUST\"></script>|" \
    -e "s|<script src=\"/app.js\"></script>|<script src=\"app.js?v=$CACHE_BUST\"></script>|" \
    -e 's|href="/style.css"|href="style.css"|' \
    web/dist/index.html > "$OUT_DIR/index.html"

if [ "${PREVIEW_RENDER_ONLY:-0}" = "1" ]; then
    echo "已生成 $OUT_DIR/index.html"
    exit 0
fi

SHOT="$OUT_DIR/$VIEW-$MODE.png"
if [ "$SCROLL" != "0" ]; then
    SHOT="$OUT_DIR/$VIEW-$MODE-scroll$SCROLL.png"
fi
if [ -n "$MODAL" ]; then
    SHOT="$OUT_DIR/$VIEW-$MODE-$MODAL.png"
fi

"$CHROME" --headless --disable-gpu --hide-scrollbars \
    --virtual-time-budget=3500 \
    --window-size=1440,900 \
    --screenshot="$PWD/$SHOT" \
    "file://$PWD/$OUT_DIR/index.html" >/dev/null 2>&1

echo "已生成 $SHOT"
