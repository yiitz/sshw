---
name: sshw
description: 使用或开发 sshw（SSH 客户端包装器）。当需要登录/连接 SSH 服务器、从主机列表选择服务器、上传或下载远程文件、登录后执行命令、编写 ~/.sshw 主机配置，或在 sshw 仓库中构建、测试、修改代码时使用。
---

# sshw

sshw 是一个交互式 SSH 客户端包装器：从 YAML 配置或 `~/.ssh/config` 读取主机，提供可搜索的主机选择列表，自动完成登录、SFTP 上传/下载、跳板机连接和命令执行。入口为 `cmd/sshw/main.go`。

## 调用约定

- 用户已指定 node（名称/别名/IP）：用 `-n <name>` 直接执行。注意 `-n` 只能传配置里的 `name`，传的是 `alias` 时先在配置中找到对应 `name`。
- 用户**没有指定 node**：直接运行 `sshw`（使用 `~/.ssh/config` 时运行 `sshw -s`），弹出主机选择列表让用户自己选。不要自行读取配置替用户挑一个，也不要用 `-n` 跳过选择。
- 当前环境没有 TTY、无法显示选择列表时：明确告诉用户原因，请其提供 node 名称或在终端里运行 `sshw`；不要静默改成 `-n`。

## 命令速查

| 目的 | 命令 |
| --- | --- |
| 交互选择主机并登录 | `sshw` |
| 按名称直接登录 | `sshw -n <name>` |
| 使用 OpenSSH 配置 | `sshw -s`（可与 `-n` 组合） |
| 登录后执行命令并退出 | `sshw -n <name> -c "uptime"` |
| 上传文件 | `sshw -n <name> -put ./local.txt [/remote/dir]` |
| 临时文件上传 | `sshw -n <name> -put ./local.txt /remote/dir -ut` |
| 下载文件 | `sshw -n <name> -get /remote/file [-o /local/dir]` |
| 版本 / 帮助 | `sshw -version` / `sshw -help` |

flag 是 Go 标准库 `flag` 风格：`-put path`、`-put=path`、`--put path` 都可以。

## 使用要点

### 选择主机

- `-n` 按配置里的 `name` 递归匹配（包括 `children`），**不匹配 `alias`**（alias 只用于列表括号展示）。
- 交互列表的搜索过滤 `name user host`；输入空格分隔的多个关键词时按 AND 匹配。
- 分组节点会进入子列表，首项 `-parent-` 返回上一级。
- `-n` 命中分组节点时不会展开，会拿该分组自身的 `host` 去连接（通常为空而失败）。要进分组请用交互模式。
- 未指定 node 时优先启动交互选择列表，让用户自己选；只有用户已给出 `name` 时才用 `-n` 跳过列表。
- 交互选择依赖 TTY；无法提供 TTY 时说明原因并请用户提供 node，不要静默改用 `-n`。

### 执行命令

- `-c` 先建立交互式登录 shell，再发送 `cmd\r` 和 `exit\r`，所以输出可能带提示符或回显，且**不转发远端退出码**（sshw 正常结束时退出 0）。
- 需要精确退出码或干净 stdout 的自动化场景，优先用系统 `ssh`。

### 文件传输

- 目标路径解析：`-put` 不指定远端时用本地文件名放到登录目录；远端路径存在且是目录时自动拼接文件名。`-get` 不指定 `-o` 时下载为当前目录同名文件；`-o` 是已存在目录时同样拼接文件名。
- 上传第一次会截断远端文件；下载会用 `os.Create` 截断本地文件。
- 断线自动重连续传：下载以本地文件长度为续传点，上传以远端文件长度为续传点；退避从 3s 起每次翻倍、上限 30s，等待期间按 Enter 立即重连。目标文件比源文件长时会清空重传。
- 续传只在**同一进程内**有效；命令退出后重跑会从头开始。
- 不存在、无权限属于永久错误，立即失败不重试；其他错误会无限重试。
- 进度条写 stderr，使用 IEC 单位；日志（含 `[sshw]` 前缀）写 stdout。
- `-ut` 先上传到 `<目标>.sshwtmp`，结束后再新开一个交互 shell 执行 `mv` 覆盖目标。该收尾步骤需要 TTY，不适合非交互脚本。
- `-put` 优先于 `-get`，两者都优先于 `-c`。

### 认证

- 私钥优先级：`keybytes` > `keypath` > 默认 `~/.ssh/id_rsa`，读不到再试 `~/.ssh/id_ed25519`。
- 配置了 `password` 会追加密码认证；始终追加 keyboard-interactive，用于 OTP/二次验证等交互问题。
- 直连认证失败且错误包含 `no supported methods remain` 时，会在终端提示输入密码再试一次。
- 主机密钥按 `~/.ssh/known_hosts` 校验：已记录的主机必须匹配；未知主机在终端提示指纹、输入 `yes` 后写入（TOFU）；密钥变化直接拒绝，非 TTY 下未知主机会失败。仍启用了部分旧加密算法。
- YAML 中的 `keypath` **不做 `~` 展开**（`-s` 模式的 `IdentityFile` 会展开），请写绝对路径。

### 配置

- 查找顺序：`~/.sshw`、`~/.sshw.yml`、`~/.sshw.yaml`，然后是当前目录下同名文件；**第一个可读的文件生效**，不会合并。
- `-s` 时忽略 sshw 配置，改为读取 `~/.ssh/config`。
- 配置中可能含明文密码，建议 `chmod 600 ~/.sshw`。
- 完整字段、示例、分组/跳板机/callback-shells 写法见 [references/config.md](references/config.md)。

## 常见问题

| 现象 | 原因 / 处理 |
| --- | --- |
| `cannot find node by alias` | `-n` 匹配的是 `name`，不是 `alias` |
| `cannot get node` | 交互选择被取消（Esc/Ctrl+C），或没有可用 TTY |
| 提示 `...'s password:` | 直连认证失败后的终端密码回退；自动化场景应配置密钥或 `password` |
| 传输刚开始就失败 | 远端路径不存在/无权限，属于永久错误，不会重试 |
| `-ut` 上传成功后报终端错误 | `mv` 收尾通过交互 shell 执行，需要 TTY |
| 连接空主机/立即失败 | `-n` 命中了分组节点，改用交互模式进入分组 |

## 开发

- 构建：`go build -o /tmp/sshw ./cmd/sshw`；冒烟：`go run ./cmd/sshw -version`。
- 校验：`go vet ./... && go build ./...`。仓库目前**没有任何测试文件**。
- mage：`mage build`（本机，产物在 `dist/`）、`mage pack`（全平台 + checksum）、`mage fmt`（只覆盖 `cmd/` 与根目录，覆盖不到 `internal/`、`pkg/`；建议用 `gofmt -s -w ./cmd ./internal ./pkg`）、`mage test`、`mage clean`。
- 发布：推送 `vX.Y.Z` tag 触发 GitHub Actions 的 GoReleaser。
- 代码结构、传输重试不变量和改动注意事项见 [references/development.md](references/development.md)。
- 提交信息沿用仓库现有的 Conventional Commits 风格（如 `fix(client): ...`，标题英文、要点列在正文）。
