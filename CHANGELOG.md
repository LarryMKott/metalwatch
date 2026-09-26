# 更新日志

本文件由 `deploy/tools/gen_release_notes.py` 自动生成，请勿手工编辑已发布版本的内容。
提交信息请遵循[约定式提交](https://www.conventionalcommits.org/zh-hans/)。

## MetalWatch v0.2.0

> 📅 发布日期：2026-09-26 · 🔢 提交数量：42 · 👥 贡献者：YiDaZhang

### ✨ 新功能

- 按最终目录架构落地后端骨架、Agent 与前端 TS 工程 (9bf9423)
- 按终版设计重整——gRPC 双向流、引擎层、前端 Arco，文档按类型重整并加进度看板 (50a96a2)
- Phase 1 数据面闭环——gRPC 双向流、时序落库、告警链路与资产变更检测 (72d8afc)
- **W11**: 管理接口鉴权与审计闭环，前端登录页 (1ea5c01)
- **W12**: FPK 构建链路与运行时冒烟，修复 -trimpath 二进制启动即死 (6eb27ef)
- **env**: 全局环境变量 METALWATCH_ENV 区分 dev/prod，各 main 无参数可运行 (5e66b91)
- **ci**: 分平台测试/编译/发布流水线，发布前验证待交付的二进制 (6d5cfb6)

### 🐛 问题修复

- 代码评审发现的问题修复（H-1/H-2 + M 系列） (eccde8d)
- 评审修复批次 6（工程链路一致性）与批次 7（ruff 清零） (45c1841)
- **ws**: 订阅登记前置于握手，堵住告警推送丢事件窗口 (b095fea)
- **build**: 打包脚本改用 Python 3.12 兼容写法，并让 CI 用自身版本执行一遍 (18a63b2)
- **fpk**: 补齐 cmd/ 九个生命周期脚本，卸载数据删除改到 uninstall_callback (128e37e)
- **agent**: 补传与实时上报串行化到同一把锁，并让每轮采集都排空积压 (f94652c)

### ♻️ 代码重构

- FPK 骨架由容器形态改为飞牛原生应用形态 (48acbbf)
- **D31**: 类型化重构与同义实现收敛（R1–R5） (d334538)

### 🧪 测试

- **py**: 新增 Python 三层测试体系与 CI 门禁 (a73872f)

### 👷 构建与流水线

- 新增 GitHub Actions 工作流；补全 Agent Windows 入口与交叉编译脚本 (94b8a95)
- **release**: 增加 dry_run 开关，零副作用验证发布链路 (4ed6066)
- **release**: 删掉 fpk job 里无效的前端产物回填 (cf5029b)
- **release**: 干跑态标在 job 名上，别让 success 被读成已发版 (799515b)

### 📦 打包构建

- 门禁改用 fmtcheck，别让 make check 先擦掉问题再判通过 (15556db)

### 📝 文档

- 补充 Git 工作流约定 (8949691)
- 记录项目长期约定与本机 git 避坑经验 (62738d9)
- 设计文档同步为无容器（native）形态 (74f94e6)
- 更新项目长期约定（native 架构）并记录 git rm -r 事故教训 (f008379)
- 记录最终目录架构、技术栈定案与本机工具链坑 (9c64d0c)
- 记录 GitHub 远程关联、代理配置与推送授权待办 (4fa4adf)
- 留档仓库审计结论；生产环境默认关闭实时告警 WS（端点待 W7 交付） (9d150d2)
- 记录 push 后 tracking ref 的修复方法与 GitHub 凭据状态 (c872579)
- 记录 Phase 1 提交前的门禁修复与 Go 测试验证经验 (f3f75af)
- 代码评审报告、文档索引与工作记忆同步 (f9c13e6)
- 工作记忆同步（提交推送记录与本机 git 三坑） (e504ce9)
- 工作记忆更正 —— push 卡住真因是凭据助手 helper-selector 挂死 (07b07b8)
- D38 与实测记录 —— CI 首个失败 job 的定性套路与修复证据 (c68403d)
- D39/D40 —— 发布链路干跑验证与九个生命周期脚本 (a961ab8)
- 补记 job 名表达式的实测渲染，并把零副作用查法补强 (f4cf6cd)
- 记 D41 —— 补传与实时上报必须串行化到同一把锁 (3187df9)
- 记 D41 —— 补传与实时上报必须串行化到同一把锁 (8744d20)

### 🔧 杂项维护

- 初始化 MetalWatch 项目骨架与设计文档 (4e4e3b4)
- 整理仓库：删除无引用代码、修两个 Agent 缺陷、修正 gitignore (c997cfb)
- 代码托管与 module 路径迁至 GitHub (dadbc30)
- **memory**: 记下「GitHub 侧求值必须真跑」与零副作用双查法 (cc27bcb)

---

**安装（飞牛 fnOS）**：下载与应用架构一致的 FPK —— x86 设备用 `metalwatch-fpk-x86_64-0.2.0.fpk`、ARM 设备用 `metalwatch-fpk-arm64-0.2.0.fpk`，在应用中心手动安装。
**安装（二进制）**：其他部署方式按平台下载 `metalwatch-server-*` 与 `metalwatch-agent-*`，WebUI 为 `metalwatch-webui-0.2.0.tar.gz`（解压后由服务端托管）。
**校验（SHA-256）**：下载附件 `SHA256SUMS`，与资产放在同一目录后执行 `sha256sum -c SHA256SUMS`（Windows 可用 `certutil -hashfile <文件> SHA256` 逐个对照）。
**变更范围**：4e4e3b4ef06d0fa4a1a58e958baf588f9dd5b6ef（根提交）..HEAD

<!-- release-baseline: 8744d20ca254a4f7f4ea706109866906396e9001 -->
<!-- release-start: 4e4e3b4ef06d0fa4a1a58e958baf588f9dd5b6ef -->
