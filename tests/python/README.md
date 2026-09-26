# MetalWatch Python 测试体系

对已完成后端功能的三层黑盒测试（冒烟 / 单元 / 集成），直接对运行中的服务端发起真实 HTTP/gRPC 交互。
仅依赖 Python 标准库 + pytest；**需要先启动本地服务端**（见下）。

> **规范（D34，强制）**：编写代码后必须编写对应的测试——新端点带单元契约用例并登记
> `mw/endpoints.py`，跨模块链路带集成用例，bug 修复先写复现用例；未带测试的代码不合入。
> CI 的 `python-tests` job 强制执行。详见 `docs/01-架构/02-关键决策.md` D34。

## 快速开始

```bash
# 1. 启动服务端（一次性；数据目录含引导管理员口令文件）
cd backend
go build -o bin-local/metalwatch.exe ./cmd/server
./bin-local/metalwatch.exe serve --data ./tmp-data --listen 0.0.0.0:18080

# 2. 跑测试
cd ../tests/python
python run_tests.py              # 全量（含约 3 分钟的离线判定集成用例）
python run_tests.py smoke        # 只跑冒烟（秒级，适合日常快速验证）
python run_tests.py unit         # 只跑单元
python run_tests.py integration  # 只跑集成
python run_tests.py all --fast   # 集成跳过 slow 用例

# 或直接用 pytest
python -m pytest smoke -v
python -m pytest unit integration -q
```

## 三层职责

| 层 | 目录 | 职责 | 耗时 |
| --- | --- | --- | --- |
| **冒烟** | `smoke/` | 核心路径存活验证：探针、登录、只读接口 200、Agent 在线。零副作用，可高频执行 | 秒级 |
| **单元** | `unit/` | 单接口契约全覆盖：正向、边界条件、输入验证、异常码。数据自建自清，用例互不依赖 | ~4s |
| **集成** | `integration/` | 跨模块协作流：注册→上报→落库→查询；告警触发→WS 推送→Webhook（HMAC）→确认→恢复；gRPC 资产快照→变更检测；Agent 启停→离线判定 | ~4min |

## Allure 报告

套件默认生成 **Allure 结果**（`reports/allure-results/`，pytest+allure-pytest）：

- 自动元数据：`feature`=套件（冒烟/单元/集成）、`story`=模块、`title`=docstring 中文标题、
  `severity`（smoke/integration=Critical，unit=Normal）——由 conftest 自动标注，用例零侵染；
- 自动附件：`client.call` 把每次 HTTP 请求/响应体挂到用例上（失败诊断直接看报文）；
- 集成用例带 `allure.step` 阶段步骤（上报→落库→推送→确认→恢复）；
- `environment.properties` 记录被测地址、服务端版本、Python 平台。

查看交互式报告（需 Java 版 Allure CLI）：

```bash
pip install allure-pytest          # 套件依赖
allure serve tests/python/reports/allure-results   # 即时渲染
# 或 run_tests.py 自动探测 CLI 生成静态报告 reports/allure-report/
```

未安装 allure-pytest 时套件自动降级为纯 pytest 模式（`--no-allure` 可强制关闭），不影响 D34 门禁。

## 测试数据隔离约定

- 主机名 / 注册码 / batch_id / webhook 渠道名均带每次运行唯一的 uuid 后缀；
- 用例内创建的资产在 teardown 中删除（告警历史按设计保留，host_id 置 NULL）；
- 集成用例断言只用自己创建的资源（唯一槽位名 / 主机名），重跑结果一致；
- 需要种子数据的场景（注册码、通知渠道）经 SQLite 直写测试库，不污染生产路径。

## 报告与通知

- `run_tests.py` 产出 `reports/report_<时间戳>.html`（自包含）+ `reports/latest.md` 摘要；
- **端点覆盖率**：`mw/endpoints.py` 登记服务端全部 REST 端点（34 个，含内嵌 WebUI 入口与 BMC 管控），运行时自动统计命中，
  报告给出百分比与未覆盖清单。新增后端路由时须同步登记；
- 通知按需启用（环境变量，未配置则跳过）：
  - 邮件：`MW_SMTP_HOST` `MW_SMTP_PORT` `MW_SMTP_USER` `MW_SMTP_PASS` `MW_NOTIFY_EMAIL_TO`
  - IM/自定义 Webhook：`MW_NOTIFY_WEBHOOK_URL`（POST JSON 文本，兼容企业微信/钉钉/飞书格式）

## 环境变量

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `MW_BASE_URL` | `http://127.0.0.1:18080` | 被测服务端 |
| `MW_DATA_DIR` | `<repo>/backend/tmp-data` | 数据目录（读引导口令 / 直写 SQLite） |
| `MW_HOOK_PORT` | `18101` | Webhook 接收器端口 |
| `MW_AGENT_HOST_ID` | `1` | 本机 Agent 的 host_id（离线用例） |

## 维护要求

1. **新增后端功能必须同步新增/更新用例**，并登记 `mw/endpoints.py`；
2. 服务端契约变更（字段名、错误码）先改契约再改测试；
3. 集成用例出现 timeout 时先排查服务端日志，再怀疑用例本身——本套件已多次抓到服务端真 bug；
4. 保持用例可重复执行：不依赖运行顺序、不产生跨用例状态。
