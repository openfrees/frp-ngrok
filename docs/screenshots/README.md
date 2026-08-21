# Screenshots for the GitHub README

English README embeds the files without a `-zh` suffix.
Chinese README embeds the `-zh` variants.

Capture on a retina Mac. Crop chrome that is not the product
(unrelated desktop, other menu extras).
Use example domains only: `web.tunnel.example.com`, never a real customer host.
Placeholder IPs must be documentation addresses such as `203.0.113.10`.

| File | What it shows | In README |
|---|---|---|
| `tunnels.png` / `tunnels-zh.png` | Tunnels tab, 2 rows, top status | yes — hero |
| `health.png` / `health-zh.png` | Health check, three layers + public reachability | yes |
| `deploy.png` / `deploy-zh.png` | Server deploy: DNS, wildcard cert, script | yes |
| `language.png` / `language-zh.png` | Settings → Language / 语言 | yes |
| `tray.png` | Menu bar — skipped. Native menus cannot be redacted; README uses an ASCII menu instead. | no |
| `wizard.png` | First-run wizard — optional. `deploy.png` already covers DNS / cert / script. | no |

`deploy.png` is the deploy plan for a connected server. It is not the
first-run wizard; keep those names separate.

Suggested width: 1280–1600 px. PNG, no mock data that looks like production secrets.
