# Kill the C —— GitHub 上传与发布指南

面向本机 `D:/workbuddy/C盘清理专家` 项目，照着做即可完成开源发布。

---

## 零、上传前状态（已替你准备好）

| 项 | 状态 |
| --- | --- |
| `.gitignore` | 已就位，排除 `.build/`、`.tools/`、`dist/`、`*.syso`、`.probe/`、`.workbuddy/` |
| `.gitattributes` | 已就位，强制 LF 换行，保证 `build.sh` 在任何平台都能跑 |
| `LICENSE` | MIT，版权人 `Wu Bage` |
| 硬编码路径 | 已清除（`build.sh`、`tools/makeicon.py` 改为自动探测） |
| `README.md` | 已改为面向 GitHub 的版本（含下载入口、构建说明、许可证） |
| 待上传体积 | **54 个文件 / 2.3 MB**（源码 + 文档 + 截图），无大文件、无敏感信息 |
| 本地 Git 仓库 | **已初始化，文件已加入暂存区** —— 你只需配置身份 → 提交 → 推送（见第三节） |

> `dist/`、`installer/payload/app.gz` 是构建产物，不入库——安装包走 **Releases** 分发，
> 仓库只放源码，clone 快、历史干净。

---

## 一、前置准备

### 1. 注册账号与安装 Git

- GitHub 账号：<https://github.com/signup>
- Git for Windows：<https://git-scm.com/download/win>（安装时全部默认即可）

装完在 **Git Bash** 里验证：

```bash
git --version
```

### 2. 设置提交身份（只需一次）

```bash
git config --global user.name "Wu Bage"
git config --global user.email "你的GitHub邮箱"
```

### 3. 让中文文件名正常显示（Windows 必做）

```bash
git config --global core.quotepath false
```

不设置的话，`git status` 会把 `使用说明.md` 显示成 `\344\275\277\347\224\250...` 这种八进制转义。

### 4. 登录凭据

Git for Windows 自带 Git Credential Manager：**第一次 `git push` 时会自动弹出浏览器让你登录 GitHub**，授权一次即可，之后免密。

---

## 二、创建远程仓库

### 方式 A：网页创建（推荐，直观）

1. 打开 <https://github.com/new>
2. **Repository name** 填 `kill-the-c`
3. **Description** 可选，例：`轻量级 Windows 系统盘清理工具 ｜ Go + WebView2 ｜ 安装包仅 5 MB`
4. 可见性选 **Public**
5. **不要**勾选 `Add a README file` / `.gitignore` / `license`（本地已有，勾了会冲突）
6. 点 **Create repository**

创建后会看到一个空仓库页面，记下 HTTPS 地址：

```
https://github.com/<你的用户名>/kill-the-c.git
```

### 方式 B：用 GitHub CLI（一条命令）

已装 `gh` 的话可以省去网页操作：

```bash
gh repo create kill-the-c --public --source=. --remote=origin --push
```

> 这条命令会一次性完成「建仓 + 关联 + 推送」。用完可以直接跳到 **第四节**。
> 没装 `gh` 就用方式 A。

---

## 三、首次提交与推送

> **本地仓库已初始化，54 个文件也已加入暂存区**，可以直接从下面的第 3 步开始。
> 下面是完整流程，若想从头来过，先执行 `rm -rf .git` 再照做即可。

在 **Git Bash** 里逐段执行。

### 1. 初始化仓库

```bash
cd "D:/workbuddy/C盘清理专家"
git init -b main
```

> 如果提示 `Reinitialized existing Git repository`，说明之前初始化过，无妨。

### 2. 确认被忽略的文件符合预期 ⚠️ 关键一步

推送前**务必**先看一眼，确认没有把几百 MB 的工具链传上去：

```bash
git status --short
```

应该**只看到**源码、文档、图片类文件。如果出现了 `.build/`、`.tools/`、`dist/`、
`app.gz` 里的内容，说明 `.gitignore` 没生效，**先停下**，检查文件名是否为 `.gitignore`
（前面有点、无扩展名）。

单独验证几个大目录确实被忽略：

```bash
git check-ignore -v .build .tools dist installer/payload/app.gz .workbuddy
```

正常输出（每条都带 `.gitignore:行号`）：

```
.gitignore:3:.build/	.build
.gitignore:4:.tools/	.tools
.gitignore:9:dist/	dist
.gitignore:10:installer/payload/*.gz	installer/payload/app.gz
.gitignore:28:.workbuddy/	.workbuddy
```

再确认一下总大小（正常应在 2 MB 上下）：

```bash
git ls-files --others --exclude-standard | wc -l      # 待提交文件数
du -sh --exclude=.git --exclude=.build --exclude=.tools --exclude=dist --exclude=.workbuddy .
```

### 3. 提交

```bash
git add .
git commit -m "feat: Kill the C 磁盘清理工具 v1.0.0

- 全盘扫描分类：16 类目 / 4 风险等级 / P0-P3 优先级
- 白名单、残留应用扫描、重复文件清理（整盘三级哈希比对）
- 轻度 / 深度双模式，所有删除二次确认，默认移入回收站
- Go 1.25 纯 syscall + WebView2，安装包 5 MB
- Win32 原生安装器，支持静默安装与卸载"
```

### 4. 关联远程并推送

```bash
git remote add origin https://github.com/<你的用户名>/kill-the-c.git
git push -u origin main
```

第一次推送会弹出浏览器登录 GitHub，授权即可。

推送成功后刷新仓库页面就能看到全部文件。

### 5. 验证

```bash
git remote -v                      # 确认远程地址
git log --oneline -1               # 确认提交
git count-objects -vH              # 查看仓库体积（正常几百 KB ~ 2 MB）
```

---

## 四、发布 Release（上传安装包）

仓库里没有 `dist/KillTheC-Setup.exe`，安装包要作为 **Release 附件**发布，这样别人能直接下载。

### 方式 A：网页发布（推荐）

1. 打开 `https://github.com/<你的用户名>/kill-the-c/releases/new`
2. **Choose a tag** 输入 `v1.0.0` → 点 `Create new tag: v1.0.0 on publish`
3. **Release title** 填 `Kill the C v1.0.0`
4. **Describe this release** 可以写更新日志，例如：

   ```markdown
   ## 首个正式版

   Windows 系统盘清理工具，安装包仅 5 MB。

   ### 下载
   下载下方 `KillTheC-Setup.exe`，双击安装即可（无需管理员权限）。

   ### 功能
   - C 盘全盘扫描，16 类目分类 + 风险等级 + 优先级排序
   - 白名单文件夹、已卸载应用残留扫描、重复文件清理
   - 轻度 / 深度双模式；所有清理操作二次确认
   - 默认移入回收站，每个功能后提供一键清空回收站

   ### 环境要求
   Windows 10 1803+ / Windows 11（自带 WebView2 运行时）
   ```

5. 把 `D:\workbuddy\C盘清理专家\dist\KillTheC-Setup.exe` **拖拽**到下方的附件区
6. 点 **Publish release**

### 方式 B：命令行发布

```bash
cd "D:/workbuddy/C盘清理专家"
gh release create v1.0.0 dist/KillTheC-Setup.exe \
  --title "Kill the C v1.0.0" \
  --notes "首个正式版：Windows 系统盘清理工具，安装包仅 5 MB。"
```

### 顺手加个 SHA-256 校验值

方便别人验证下载的安装包没被篡改：

```bash
sha256sum dist/KillTheC-Setup.exe | tee dist/KillTheC-Setup.exe.sha256
```

把生成的 `.sha256` 文件也作为附件上传，并在 Release 说明里贴出哈希值。

---

## 五、后续更新流程

改完代码后：

```bash
cd "D:/workbuddy/C盘清理专家"

# 1. 构建（可选，确认改动没问题）
bash build.sh

# 2. 提交
git status --short          # 先看一眼改了什么
git add .
git commit -m "fix: 修复 XXX 问题"
git push

# 3. 有版本更新时，打标签并发 Release
git tag -a v1.0.1 -m "Kill the C v1.0.1"
git push origin v1.0.1
gh release create v1.0.1 dist/KillTheC-Setup.exe --title "Kill the C v1.0.1" --notes "更新说明…"
```

> 记得同步更新 README 里写的版本号和体积数字，以及 `软件版本` 相关的描述。

---

## 六、常见问题

### 不小心把大文件提交了

比如误把 `.tools/` 提交进去，仓库瞬间膨胀几十 MB：

```bash
# 从 Git 索引移除（保留本地文件）
git rm -r --cached .tools
git commit -m "chore: 移除误提交的工具链"
git push
```

如果已经推送到远程，历史里仍有大文件。彻底清理需要 `git filter-repo`：

```bash
pip install git-filter-repo
git filter-repo --path .tools --invert-paths --force
git remote add origin https://github.com/<你的用户名>/kill-the-c.git
git push --force
```

> `--force` 会重写远程历史，**仅在确认这是自己的个人仓库时使用**。

### `git status` 显示中文文件名是乱码

```bash
git config --global core.quotepath false
```

### 推送时提示 `Authentication failed`

GitHub 已不支持账号密码推送。两个办法：

- 用 Git Credential Manager（浏览器登录）——干净的仓库克隆地址用 HTTPS 即可
- 用 Personal Access Token：`Settings → Developer settings → Personal access tokens → Tokens (classic)`，
  勾选 `repo` 权限，生成后把 token 当作密码使用

### 提示 `remote origin already exists`

说明之前关联过：

```bash
git remote set-url origin https://github.com/<你的用户名>/kill-the-c.git
```

### 换行符警告 `LF will be replaced by CRLF`

项目已带 `.gitattributes`，正常情况下不会出现。若仍出现：

```bash
git config --global core.autocrlf input
```

### 想确认远程仓库到底有多大

```bash
git count-objects -vH
```

关注 `size-pack` 一行。当前项目应在 1 MB 左右。

### `.workbuddy/` 被忽略了，但我想保留开发日志

删掉 `.gitignore` 最后那三行即可：

```bash
# 编辑 .gitignore，删除以下内容
# ── 本地助手记忆（含本机路径等私有信息，不入库）────────────────────
.workbuddy/
```

> 注意：该目录含本机路径与开发过程记录，公开前请自行确认内容是否适合发布。

---

## 七、发布后建议

- **补一个截图**：README 里已引用 `docs/images/` 下的 12 张截图，GitHub 会自动渲染
- **开启 Issues**：`Settings → General → Features → Issues`
- **设置 Topics**：仓库首页右上角齿轮，填 `windows` `disk-cleanup` `golang` `webview2` `system-optimization`
- **加个 About 简介**：同位置，一句话描述 + 官网/Release 链接

---

## 附：一页速查

```bash
cd "D:/workbuddy/C盘清理专家"
git config --global core.quotepath false
git init -b main
git status --short                              # ⚠️ 确认没有大文件
git check-ignore -v .build .tools dist          # ⚠️ 确认忽略生效
git add .
git commit -m "feat: Kill the C v1.0.0"
git remote add origin https://github.com/<你的用户名>/kill-the-c.git
git push -u origin main
# 然后到 Releases 页面发布 v1.0.0 并上传 dist/KillTheC-Setup.exe
```
