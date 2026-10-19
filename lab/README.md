# 本地靶场(P3-04)

GhostReaper 的授权测试靶场。**主力 DVWA**(一个容器一个网站、十几种
漏洞并存、稳定地址,贴合"一个任务持续深挖"模型),**Vulhub 补充**
(特定高危漏洞按需起、打完即关)。仅供授权测试使用。

## DVWA 主力

一条命令起(缺镜像自动构建,自动初始化):

```sh
make dvwa-up        # 等价 lab/dvwa.sh up
```

| 项 | 值 |
|----|----|
| 地址 | `http://127.0.0.1:4280`(仅本机回环) |
| 凭据 | `admin` / `password` |
| 安全等级 | 默认 `low`(登录后可在 DVWA 的 DVWA Security 页改 low/medium/high/impossible) |
| 容器 | `ghostreaper-dvwa`(镜像 `ghostreaper-dvwa:latest`) |

初始化语义:`make dvwa-up` 每次自动完成——登录(拿会话与
anti-CSRF token)→ `setup.php` Create / Reset Database(建表;已建则
如实提示已存在)→ 本会话安全等级设 `low`。镜像内 MariaDB 随容器起,
幂等建库 `dvwa` 与用户 `dvwa`/`p@ssw0rd`(仅容器内,不对外)。

版本钉死:源码取官方 `digininja/DVWA` master,commit
`068d974487a297c155c48722f9fcf7180a83adc4`(见 `lab/dvwa/Dockerfile`
的 `DVWA_SHA`,升级 = 换 SHA 重建)。定制两处:默认安全等级 `low`
(官方默认 impossible)、`allow_url_include=On`(文件包含模块需要,
官方生产默认关)。

并存漏洞模块(登录后左侧菜单):SQL 注入 / SQL 盲注 / XSS 反射·存储·
DOM / 命令注入 / 文件包含(LFI+RFI)/ 文件上传 / 爆破 / CSRF / 不安全
验证码(未配 Google key 时该模块不可用)/ 弱会话 / 开放重定向 / CSP
绕过 / JS 挑战 / API(新版)。够"一个任务持续深挖"。

其他子命令:

```sh
make dvwa-down      # 停容器(保留,下次 up 秒起,数据保留)
make dvwa-status    # 容器状态 + 站点可达 + 当前安全等级
make dvwa-reset     # 删容器重建(库表重置,数据全清)
make dvwa-clean     # 删容器 + 删镜像(收尾清理)
```

## 标准授权目标登记(RoE + 种子资产)

DVWA 地址自 P3-04 起登记为**标准授权目标**,此后 P3 各【靶场】阶段
默认对它验收(造物主当次另有提供时以其为准)。以它建任务时:

- **种子资产**(建任务表单 → 资产):类 `服务`,标识
  `http://127.0.0.1:4280`(表单批量粘贴识别 http(s) 地址自动归服务类);
- **RoE**(建任务表单 → 授权边界):备注写明
  `授权范围:127.0.0.1:4280(本地 DVWA 靶场)`——授权范围靠边界与
  审批约束,不靠工具白名单收紧;
- 目标凭据 `admin/password` 属目标配置,按 P3-09 凭据管理落正式
  配置(此前临时记入任务描述或 RoE 备注)。

## Vulhub 补充

测特定高危漏洞时按需起、**打完必关**:

```sh
make vulhub-fetch                      # 一次性下载官方仓库 tarball(不用 git)
make vulhub-up VULHUB_ENV=nginx/CVE-2021-23017   # 起(环境名见 lab/vulhub.sh list)
make vulhub-down VULHUB_ENV=nginx/CVE-2021-23017 # 关并清理
```

环境地址以该环境 compose 的端口映射为准(`up` 后会列出;利用方法见
各环境目录 README,或 https://vulhub.org )。

## 清理

```sh
make dvwa-clean      # DVWA: 删容器 + 删镜像
lab/vulhub.sh clean  # Vulhub: 删本地仓库与缓存
```

注意:Vulhub 环境镜像(各 CVE 镜像)按 Docker 镜像留存,空间紧张时
`docker image prune -a` 清(会连带清掉其他未使用镜像,慎用)。
