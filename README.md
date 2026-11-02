# ClashScale 🐶

**代理（Mihomo）+ 组网（Tailscale / Headscale）合一的 Windows 客户端。**
由 GoProxyParty（对标 mihomo-party）与 Win_Tailscale（对标官方 Tailscale）整合而来，
Wails v2 + 原生 Web 前端，无需 Node 构建。

## 核心理念：独立控制，但天生协作

| | 代理 · Mihomo | 组网 · Tailscale |
|---|---|---|
| 开关 | 总览页左卡片的电源键 | 总览页右卡片的电源键 |
| 自启 | 「启动时自动运行内核」 | 「启动时自动运行服务 / 自动连接」 |
| 退出行为 | 退出即停止并清理系统代理 | 可选择退出时保留服务 |

任何一边都可以单独使用、单独启停；总览页还有 **全部关闭 / 仅代理 / 仅组网 / 全部开启** 四个场景一键切换。
托盘图标右下角是**双色状态点**：左半 = 代理，右半 = 组网。

## 超出预期的地方

**1. 组网智能适配（默认开启）** —— 解决 Clash 与 Tailscale 同时使用的真实痛点：
- 自动在规则最前面注入 `100.64.0.0/10`、`fd7a:115c:a1e0::/48`、`*.ts.net`、你的 MagicDNS 后缀、自建 Headscale 服务器，全部直连，100.x 设备不会再被代理吞掉。
- 自动给 Mihomo 的 `dns.nameserver-policy` 加上 `100.100.100.100`，让走代理端口的程序（git、终端、IDE）也能解析 `xxx.ts.net`。
- 系统代理的「不走代理」列表补全了 `100.64.*`–`100.127.*`（原版只写了 `100.64.*`，漏了大半个 CGNAT 网段），并带上 MagicDNS 域名。
- 切换服务器 / 后缀变化时自动同步，无需手动改规则。

**2. 一键体检** —— 管理员权限、内核 / 规则库 / Tailscale 组件、端口占用、系统代理残留、经代理真实访问外网、Tailscale 健康告警、两边协作状态；能修的直接给「一键修复」按钮。

**3. 一键下载 Tailscale 组件** —— 从官方 MSI 里提取 `tailscale.exe / tailscaled.exe / wintun.dll`（不安装服务）。Clash 在运行时优先借道代理下载。

**4. 对标官方 Tailscale 客户端**：设备列表（直连 / 中继 DERP 区域、在线状态）、Ping（延迟与路径）、出口节点（使用 / 共享）、接受子网路由、MagicDNS 开关、屏蔽入站、Taildrop 收发文件、远程桌面一键拉起、netcheck 网络诊断、服务器配置（官方 / Headscale）独立保存登录状态、Auth Key 登录。偏好由 ClashScale 记住，每次 `tailscale up` 都显式带上，重连不丢设置。

**5. 对标 mihomo-party**：订阅（含流量用量 / 到期时间、定时自动更新、本地 YAML 导入）、节点测速与选择、自定义规则（排序 / 开关）、规则与全局 / 直连模式、系统代理、终端代理命令、实时速率图。

**6. 稳健性**：
- Job Object：ClashScale 无论怎么退出（含崩溃、被结束任务），mihomo / tailscaled 都会被系统一并回收，不会留下占端口的孤儿进程。
- 启动前检测端口占用，给出明确提示。
- 系统代理直接读写注册表，关机 / 注销钩子里也来得及清理，不会出现「重启后上不了网」。
- 开机自启用任务计划程序（最高权限），不弹 UAC。（普通 `Run` 注册表项无法启动需要管理员权限的程序。）

## 构建

环境：Windows 10/11、Go ≥ 1.26、[Wails CLI v2](https://wails.io)（`go install github.com/wailsapp/wails/v2/cmd/wails@latest`）、WebView2 运行时（Win11 自带）。

```bat
build.bat        :: 等价于 go mod tidy + wails build
dev.bat          :: wails dev 热更新调试
```

产物：`build\bin\ClashScale.exe`。程序以管理员身份运行（清单里已声明），这是创建 Tailscale 虚拟网卡所必需的。

## 数据目录

便携模式，数据都在 exe 同级 `data\` 下（目录不可写时回退到 `%AppData%\ClashScale`）：

```
data\app.json        外壳偏好
data\clash\          mihomo.exe、geo 文件、settings.json、config.yaml、profiles\
data\tailscale\      config.json、tailscaled.state、bin\（tailscale*.exe、wintun.dll）、logs
data\webview\        WebView2 缓存
```

**从旧版迁移**：把 GoProxyParty 的 `data` 内容复制到 `data\clash\`；把 Win_Tailscale 的 `data` 内容复制到 `data\tailscale\`（`webview` 子目录不用复制）。订阅与登录状态原样沿用。

## 源码结构

```
main.go / app.go        窗口、生命周期、偏好、一键场景
clash*.go               代理模块（进程、控制接口、配置生成、订阅、下载）
tailnet*.go             组网模块（tailscaled、状态轮询、偏好、Ping、Taildrop、组件安装）
doctor.go               一键体检
tray.go                 托盘（双色状态点）
winsys.go               系统代理、自启任务、Job Object、关机钩子
frontend/               原生 HTML/CSS/JS（js/mascot.js 为小狗吉祥物）
build/                  图标（appicon.png、trayicon.png、windows/icon.ico）、清单
```

## 说明

- 本版本的 Go 后端已针对 `windows/amd64` 交叉编译并通过 `go vet`；前端用模拟后端跑通了全部页面且无脚本错误。
  但开发环境不是 Windows，**没有在真机上运行过**——涉及真实 `tailscale` CLI 的行为（`tailscale set/up` 参数、Taildrop、MSI 解包）和托盘 / 系统代理，请首次使用时留意并反馈。
- 未包含 TUN 模式（与 Tailscale 的虚拟网卡同时使用需要更细致的路由处理，建议后续单独做）。
