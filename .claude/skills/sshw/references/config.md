# sshw 配置参考

配置文件是一个 YAML 列表，每个元素是一个主机节点。

## 查找顺序

依次尝试，第一个读取成功的文件生效（不合并）：

1. `~/.sshw`
2. `~/.sshw.yml`
3. `~/.sshw.yaml`
4. `./.sshw`
5. `./.sshw.yml`
6. `./.sshw.yaml`

使用 `-s` 时不读取以上文件，改为读取 `~/.ssh/config`（见文末）。

## 节点字段

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `name` | string | — | 建议必填。列表显示名，也是 `-n` 匹配的名称 |
| `alias` | string | — | 仅在选择列表中括号展示；`-n` 不匹配它 |
| `host` | string | — | 主机名/IP。作为跳板机节点时是跳板机地址 |
| `user` | string | `root` | 登录用户 |
| `port` | int | `22` | 小于等于 0 时按 22 |
| `keypath` | string | `~/.ssh/id_rsa` → `~/.ssh/id_ed25519` | 私钥路径。**不做 `~` 展开**，写绝对路径 |
| `keybytes` | string | — | 内联 PEM 私钥内容，优先于 `keypath` |
| `passphrase` | string | — | 私钥口令 |
| `password` | string | — | SSH 密码。明文存储，注意文件权限 |
| `children` | []Node | — | 子节点/分组；交互模式选择该节点会进入子列表 |
| `jump` | []Node | — | 跳板机列表，**只使用第一个**；跳板机节点自身的 `jump` 不会递归生效 |
| `callback-shells` | []CallbackShell | — | 登录后自动发送的按键/命令 |

`CallbackShell`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `cmd` | string | 登录后以 `cmd\r` 形式发送。YAML 中的数字也可直接写（会转成字符串） |
| `delay` | int | 发送前的等待时间，单位毫秒；省略为 0 |

## 示例

### 最小配置

```yaml
- name: dev
  host: 192.168.8.35
```

用户默认 `root`，端口默认 22，私钥自动尝试 `~/.ssh/id_rsa`、`~/.ssh/id_ed25519`。

### 密码 / 私钥 / 别名

```yaml
- name: dev password
  user: appuser
  host: 192.168.8.35
  port: 22
  password: "123456"

- name: dev key
  user: appuser
  host: 192.168.8.35
  keypath: /Users/me/.ssh/id_ed25519

- name: dev passphrase key
  user: appuser
  host: 192.168.8.35
  keypath: /home/me/.ssh/id_rsa
  passphrase: abcdefghijklmn

- name: 带别名的机器
  alias: dev
  host: 192.168.8.35
```

### 跳板机

```yaml
- name: internal server
  user: appuser
  host: 10.0.0.5          # 目标内网地址
  port: 22
  jump:
    - user: appuser
      host: 192.168.8.36  # 跳板机
      port: 2222
      password: "123456"
```

跳板机节点支持 `keypath`、`keybytes`、`passphrase`、`password` 等认证字段，但只取列表第一个元素，且不会继续嵌套跳板。

### 分组

```yaml
- name: 生产环境
  children:
    - { name: web-1, user: root, host: 192.168.1.2 }
    - { name: web-2, user: root, host: 192.168.1.3 }
```

交互模式下选择分组进入子列表，首项 `-parent-` 返回上一级。`-n` 不会展开分组。

### 登录后自动输入（callback-shells）

```yaml
- name: menu server
  user: appuser
  host: 192.168.8.35
  password: "123456"
  callback-shells:
    - { cmd: 2 }                 # 立即发送 "2"
    - { delay: 1500, cmd: 0 }    # 等待 1.5 秒后发送 "0"
    - { cmd: "cd /var/log" }     # 再发送命令
```

适合跳过登录菜单、自动回答 sudo 提示、登录后自动进入目录等场景。

## `-s`：使用 `~/.ssh/config`

- 遍历每个 `Host` 块，取**第一个** pattern 作为 `name`/`alias`。
- 读取 `HostName`、`User`、`Port`（缺省 22）、`IdentityFile`（会展开 `~`）。
- 没有 `HostName` 的块会被跳过，所以 `Host *` 这类默认块通常不会出现在列表里。
- 不支持 `ProxyJump` / `ProxyCommand` / `Include` 的跳板语义；需要跳板请在 sshw YAML 配置中使用 `jump`。
- 不读取密码，只支持密钥和 keyboard-interactive。

示例 `~/.ssh/config`：

```sshconfig
Host prod-web
    HostName 10.0.0.10
    User deploy
    Port 22
    IdentityFile ~/.ssh/id_ed25519
```

对应命令：`sshw -s -n prod-web`。
