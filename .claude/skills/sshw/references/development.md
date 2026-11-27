# sshw 开发参考

## 环境与常用命令

- Go 1.26+（`go.mod` 中 `go 1.26.0`），模块路径 `github.com/yiitz/sshw`。
- 构建并冒烟：

```bash
go build -o /tmp/sshw ./cmd/sshw
/tmp/sshw -version
```

- 提交前校验（仓库目前没有测试文件）：

```bash
gofmt -s -w ./cmd ./internal ./pkg
go vet ./...
go build ./...
go test ./...
```

- mage 目标（`magefile.go`）：
  - `mage build`：构建本机平台，产物在 `dist/sshw-<os>-<arch>-<tag>/sshw`，并打 tar.gz。
  - `mage pack`：交叉编译 darwin/freebsd/linux/windows 的 amd64/386 等组合，随后生成 `dist/checksum.txt`。
  - `mage fmt`：只对 `cmd` 目录（非递归）和根目录 `*.go` 执行 gofmt，**覆盖不到 `internal/`、`pkg/`**。
  - `mage test`：`go test -v -coverprofile` + 覆盖率报告，最后删除 `.cover.out`。
  - `mage clean`：删除 `dist/` 与 `.cover.html`。
- 发布：推送形如 `v1.2.3` 的 tag 触发 `.github/workflows/release.yml`，由 GoReleaser 按 `.goreleaser.yml` 构建并上传。注意 workflow 中的 `setup-go` 仍固定 Go 1.17，而 `go.mod` 要求 1.26；改动发布流程时要一并核对。

## 代码结构

| 路径 | 职责 |
| --- | --- |
| `cmd/sshw/main.go` | flag 定义、配置加载、交互选择（promptui）、`findAlias`、按 flag 分发 `Shell` / `SendFile` / `GetFile` |
| `internal/config/config.go` | `Node`/`CallbackShell` 模型，YAML 配置与 `~/.ssh/config` 加载，默认值辅助方法 |
| `internal/client/client.go` | SSH 认证与连接（含跳板机）、交互 shell、SFTP 上传/下载、断线重连续传、进度条 |
| `internal/client/wait_unix.go` | 重连退避时等待超时或 Enter（`unix.Poll`） |
| `internal/client/wait_other.go` | 非 Unix 平台的等待实现（只 sleep） |
| `pkg/log/log.go` | 输出到 stdout 的带 `[sshw]` 前缀日志；`Fatal` 直接 `os.Exit(1)` |
| `cmd/mage/` | 构建/打包/格式化/测试自动化 |

## 关键实现与不变量

### 启动与分发

- `-version`、`-help` 在加载配置之前返回。
- 配置加载：`-s` 走 `LoadSshConfig`，否则 `LoadConfig`。
- 分发顺序固定：`-put` > `-get` > `-c`/交互 shell。
- `-n` 通过 `findAlias` 递归搜索 `Name`；`Alias` 只用于列表模板展示。

### 连接与认证（`Connect` / `genSSHConfig`）

- 私钥顺序：`KeyBytes` > `KeyPath` > `~/.ssh/id_rsa` → `~/.ssh/id_ed25519`；解析失败只记日志，继续尝试其他认证方式。
- 认证方法顺序：公钥、密码（若有）、keyboard-interactive（始终最后追加）。
- `HostKeyCallback` 由 `newHostKeyCallback()`（`internal/client/hostkey.go`）提供，按 `~/.ssh/known_hosts` 校验：已记录的主机必须匹配，未知主机交互确认后追加（TOFU），密钥变化拒绝，非 TTY 下未知主机直接失败。
- `Ciphers` 在 `SetDefaults()` 后追加了一批旧算法。
- `jump` 只取第一个跳板机，先拨号跳板机，再通过它 `Dial` 目标；跳板机节点自身的 `jump` 不递归。
- 终端补输密码的回退只发生在直连路径，且错误需包含 `no supported methods remain` 且不含 `password`。
- `Shell`：`term.MakeRaw` + PTY（xterm），每秒轮询窗口大小，每 10 秒发 `keepalive@openssh.com`；先发送 `callback-shells`，再发送 `-c` 的 `cmd\r` 与 `exit\r`；`session.Wait()` 的返回值被忽略。
- 因为 `MakeRaw` 要求 TTY，`Shell` 在非 TTY 环境会 `Fatal`；`-ut` 的收尾 `mv` 也走 `Shell`，所以同样需要终端。

### 文件传输重试

- 下载：本地文件句柄在循环外 `os.Create` 一次（首次会截断）；重试时 `Seek(0, io.SeekEnd)` 得到续传点。若已知远端大小且本地更长，先 `Truncate(0)` 再从头传。完成后校验 `offset == srcSize`。
- 上传：首次 `sftp.Create`（截断远端），重试改用 `OpenFile(O_WRONLY|O_CREATE)` 并从远端文件末尾续传；远端更长时 `Truncate(0)`。完成后校验 `offset == total`。
- 续传状态只在进程内保存；进程退出后重跑会重新开始。
- `dropConnection` 依次关闭 sftp、目标文件、ssh 连接，让下一次循环从新连接开始。
- 重试退避 `transferRetryDelay`：第 1 次 3s、第 2 次 6s、第 3 次 12s、第 4 次 24s、之后封顶 30s；没有最大重试次数。
- 永久错误 `isPermanentTransferError`：`os.ErrNotExist`、`os.ErrPermission`，直接失败；其余错误重试。
- `waitEnterOrTimeout`：非 TTY 只 sleep；TTY 用 `unix.Poll` 等待输入，读到 Enter 后把输入排空，避免回放到后续会话。
- 进度条：`newTransferBar` 中 `OptionUseIECUnits(true)` 必须在构造时传入；创建后再 `Set` 不会改写已经格式化好的总量单位。
- `-ut` 的上传目标追加 `.sshwtmp`，`defer` 中通过新连接执行 `mv` 覆盖真实目标。

### 配置加载

- `LoadConfigBytes` 先按顺序找家目录文件，再找当前目录文件，返回第一个可读内容。
- `LoadSshConfig` 使用 `kevinburke/ssh_config` 的 `cfg.Hosts`，每个 Host 块取第一个 pattern；`os.Open`/`Decode` 的错误目前被忽略，文件缺失时会得到空主机列表而不是报错。只想保留有 `HostName` 的块。
- `Node.GetUser` 默认 `root`，`GetPort` 默认 `22`。
- `CallbackShell.Delay` 是 `time.Duration`，但发送前乘以 `time.Millisecond`，所以 YAML 里写整数即毫秒。

### 平台与日志

- 终端/系统调用相关代码用 build tags 区分：`wait_unix.go`（darwin/dragonfly/freebsd/linux/netbsd/openbsd）与 `wait_other.go`。新增平台相关代码要同时补 stub，否则交叉编译会失败。
- `pkg/log` 的 `Fatal` 会 `os.Exit(1)`；传输循环里大量错误是记录后继续重试，改错误处理时注意不要破坏重试语义。
- 进度条写 stderr，日志写 stdout；脚本解析输出时要区分。

## 修改时的检查清单

- 改 flag：同步更新 `README.md` 的 Usage 段。
- 改传输逻辑：保证下载/上传各自的“续传点”来源不变，并手动验证断线重连（可用 `kill` 掉连接或断网）。
- 改配置字段：同步 `internal/config` 的 struct tag、README 示例和本 skill 的 `references/config.md`。
- 改终端行为：在 macOS/Linux 上用真实 TTY 验证；非 TTY 下 `-put`、`-get`（不带 `-ut`）应仍可用。
- 提交信息：沿用 Conventional Commits，例如 `feat(client): ...`、`fix(client): ...`、`chore(deps): ...`；标题英文，正文用要点说明动机与行为变化。
