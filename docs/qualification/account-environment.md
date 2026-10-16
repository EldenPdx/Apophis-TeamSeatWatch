# 账号独立浏览器环境资格验证

2026-10-12；[规格 #8](https://github.com/xft0202/Apophis-TeamSeatWatch/issues/8)、[资格任务 #9](https://github.com/xft0202/Apophis-TeamSeatWatch/issues/9)。**ShardX 152.0.7977.65 的本地资格验证通过；业务接入与真实登录由依赖任务 #10 验收。** 十个无凭据 fixture 是当前这轮资格测试的样本范围；通过后继续完整业务接入，生产可管理账号数量和后续验证范围按业务规格与用户确认推进。本阶段没有使用真实账号、生产数据库或密钥，也没有使用唯一空间管理母号。

## 固定制品与唯一配方

| 项目 | 固定值 |
| --- | --- |
| 官方项目源码证据 | [ShardBrowser](https://github.com/ProxyShard/ShardBrowser/tree/4880c3fd1a6360bc205daa5bc5542b16277201c3)，commit `4880c3fd1a6360bc205daa5bc5542b16277201c3`；不是浏览器内核源码 |
| 内核 | [官方 ShardX-Linux.zip](https://pub-e57a7c60f6934eb09a6600bf2fc59cdc.r2.dev/ShardX-Linux.zip)，实际 `--version` 为 `ShardX 152.0.7977.65`；压缩 207,284,858 bytes，解压普通文件 635,544,234 bytes |
| ZIP SHA256 | `3ead1629a1c893f248cd01ee7c9125552b2cac5c91846cb71d6cb1b24883c213` |
| chrome SHA256 | `094fa2e6205bfb3d45fe1c4e7479b1e248d088895b8285a4c9ddab48c9e1100e` |
| 官方指纹库 | [ShardX-Fingerprints.zip](https://pub-e57a7c60f6934eb09a6600bf2fc59cdc.r2.dev/ShardX-Fingerprints.zip)，SHA256 `6ed51c9571b0100a5fa56b578cbc6e118b3d47eb5182759d66be4f55712bb66f`；基础配方 `shardx-fingerprints/linux-hd520.json` |
| 启动方式 | Go/chromedp 直接启动官方二进制，`--fingerprint-profile=<recipe.json>` 与 CDP；没有 SDK 安装、桌面 launcher 或管理 daemon |
| 验证环境 | 4 vCPU、7.8 GiB RAM 主机；复用镜像 `apophis-teamseatwatch:mother-delete-6ed5e36`，ID `sha256:62ff8307360ef9295921d22554ce5910a34062054726dcc132e6dddcedc68d11`；Tini/Xvfb、`--network none --rm --shm-size 256m` |

CDN manifest 与下载响应 ETag 一致：内核 `3116458af9948b31324980680f7f1194-25`、指纹库 `e2f22d25690c2590b3e087a79e141bf2`。上表 SHA256 为本地下载后计算值；官方未提供独立 SHA256，multipart ETag 不能冒充内容 checksum。

[官方许可说明](https://github.com/ProxyShard/ShardBrowser/blob/4880c3fd1a6360bc205daa5bc5542b16277201c3/README.md#licensing)区分 MIT launcher 与免费闭源内核，允许 multi-accounting；内核禁止逆向、修改后再分发，以及用作商业 anti-detect/browser/fingerprint-spoofing 产品或服务。本次只使用未修改官方二进制，不能把免费内核描述为开源。

`tools/qualification/create-shard-recipes.py` 校验官方指纹 ZIP SHA256、版本，为当前资格测试生成十份固定 fixture 配方；已有配方必须逐字一致。设备字段采用 Linux HD520 官方成套配置：CPU 4、deviceMemory 8、1366×768、语言 `[en-US,en]`、Accept-Language `en-US,en;q=0.9`、UTC。每个 `fixture-001` 至 `fixture-010` 使用独立目录；各 noise slot 的 seed 按官方 SDK 的 FNV-1a32 派生：输入 `<profileID>::<slot>`，offset 2166136261、prime 16777619，零值替换为 1。

Canvas、Audio、fonts、client_rects 原生 noise 开启且 seed 固定，client_rects max_offset=1；sensors 关闭。**WebGL 可选 readback pixel noise.enabled=false**，完整 API、GPU metadata、limits、extensions 保留，包内 SwiftShader 使用 `--use-angle=swiftshader --enable-unsafe-swiftshader`。账号复合差异来自实际观测的其他原生表面，不要求每一个 GPU/UA 字段唯一。WebRTC block、QUIC disabled；网络使用现有 `egress.BrowserRoute`。磁盘/媒体 cache 建议值为 16 MiB/4 MiB，[Chromium 开关定义](https://chromium.googlesource.com/chromium/src/+/0e0b9771cc4fe496403a49126ec7cfa6c422a6d0/chrome/common/chrome_switches.cc)说明 cache 限制是建议值，不能视为整个档案目录的硬上限。

## 验收证据

原始资料位于 `/tmp/tsw-account-environment/`；下表 `runtime-evidence/` 均相对于其中的 `shard-followup/`。所有长命令在执行前保存 worktree、源码 hash、命令、环境与待完成范围，完成后记录 exit/counts。

| 验收项 | 结果与证据 |
| --- | --- |
| 当前 Go/CDP 与业务 helper | 单并发 1/1、五并发 5/5 PASS；真实调用 `personalBrowserTransport.navigate/fetch`、`captureBrowserFetch`、`allowedRequest`、`egress.StartBrowserRoute`，覆盖文档 302、JS evaluate、fetch 200、manual/opaque 302 receipt、禁止业务路径与准确两次 POST；`runtime-evidence/{single,five}-accepted-fixture-*.log` |
| 页面/worker 一致性 | 五个 native fixture 的 Navigator、UAData、语言/时区、Canvas 像素/TextMetrics、WebGL metadata/limits/extensions 断言通过；非空文字宽度 125.15–126.00，page/worker 相同。两处连续 WebGL readPixels 差异均为 0；`five-accepted-fixture-*.json` |
| HTTP 身份一致性 | UA 与 page/worker 相同，UA 的主版本为 152；高熵 UAData/HTTP CH 完整版本 `152.0.7977.65` 与二进制相同，Linux/x86/64、语言与时区成套一致 |
| 不同档案实际复合差异 | 五个完整 native 样本 Canvas 像素 hash 5/5 不同、Audio hash 5/5 不同；十个轻量初始化样本实际 Canvas hash 10/10 不同。两个模式图案不同，不跨模式比较 hash |
| 同号重启稳定与存储 | fixture-001 在不同 hostname 新容器重启后，整个 page/worker、fontDiagnostic、noiseRecipe 逐项相同，Cookie/localStorage 恢复自身 ID；`five-accepted-fixture-001.json` 与 `single-accepted-fixture-001.json` |
| 十档案隔离及复用 | `ten` 10/10 PASS：001–005 恢复自身存储，首次启动的 006–010 Cookie/localStorage 为空；`ten-process-detail` 再次 10/10 PASS，十个都恢复自身 ID，Canvas/字体观测逐项稳定。本轮资格始终只使用这十个无凭据档案 |
| 有界执行与退出 | 两批各五个；带 pid/ppid/exe/cmdline/start_ticks 的最终进程采样独立 Chrome 根进程峰值 5；`ten-process-detail-resource-samples.json`。最终无验证容器、ShardX 进程或当前十档案 Singleton 锁；`shard-acceptance.json` |

最终聚合 gate：`evidence/qualification-shard-acceptance.json`，exit 0，4 项证据检查 PASS；检查脚本及 hash 随 `shard-qualification-snapshot.tar.gz` 固定。它验证跨轮返回稳定、十档案隔离、资源 gate 状态及当前清理状态，不启动浏览器。

## 资源观测

采样间隔 0.1 秒。单并发容器 memory/swap 2 GiB、pids 512；五并发与十档案容器 3 GiB、pids 1024。十档案仅做真实浏览器初始化、存储与 Canvas，未重复完整 native/CDP 套件。

| 指标 | 单并发完整 fixture | 五并发完整 fixture | 十档案两批初始化/复用 |
| --- | ---: | ---: | ---: |
| 测试结果 | 1/1 PASS | 5/5 PASS | 10/10 PASS |
| 容器 wall 秒 | 6.783 | 24.812 | 29.288 |
| cgroup memory.peak bytes | 318,631,936 | 1,489,928,192 | 1,226,145,792 |
| Chrome 树 RSS 累计峰值 bytes | 1,162,309,632 | 6,233,546,752 | 5,967,925,248 |
| Chrome CPU 秒（采样累计） | 11.54 | 64.15 | 78.98 |
| cgroup CPU 秒 | 12.106 | 66.654 | 83.479 |
| cgroup 采样区间 CPU 峰值（核） | 3.278 | 3.417 | 3.413 |
| Chrome 进程峰值 | 9 | 50 | 50 |
| Chrome 主进程/根进程峰值 | 1 | 5 | 5 |
| cgroup pids 峰值 | 111 | 590 | 593 |
| OOM kill / 采样错误 | 0 / 0 | 0 / 0 | 0 / 0 |
| 该轮后所有档案普通文件总 bytes | 26,078,550（已有五档案） | 26,058,023（五档案） | 44,469,957（十档案） |

资源 JSON 对应 `single-accepted-resources.json`、`five-accepted-resources.json`、`ten-process-detail-resources.json`。CPU 峰值从已有相邻样本按 `Δusage_usec / 1e6 / Δelapsed_seconds` 离线计算，结果单独存为 `shard-followup/cpu-interval-peaks-derived.json`，保留原 raw 与 summary。该值是实际采样区间平均占用的最大值，包含 browser、fixture/route、Xvfb 与 runner 开销，不能视为瞬时或浏览器独占峰值。单并发启动观测 1.130 秒；五并发各启动观测 4.011–6.274 秒；startup 包含本地 fixture/route 与 CDP 初始化，不是独立内核启动基准。

**RSS 累计会重复统计共享页**，不能把 6.23 GB 当作五并发所需物理 RAM；cgroup 内存还包含计费文件缓存。CPU 使用明显，五并发 fixture 平均约占 2.69 CPU 核，但测试不是目标平台登录吞吐量。本阶段没有 50/100 真实账号容量证据，不能据此保证这台服务器的实际登录速度或线性推算资源。空闲档案本轮退出后没有浏览器进程；最终业务并发须由代理管理运行参数控制，实验的五并发不是另设生产固定上限。

## 保留的失败与限制

- fingerprint-chromium 150 的原生 Canvas 非空 TextMetrics 负值资格 FAIL 已淘汰；原报告 `qualification.md` 与 `qualification-snapshot.*` 保留，旧候选不再诊断或下载。
- ShardX 初次全部 noise 开启时 WebGL readback 随读取变化；`shard-followup/webgl-noise-on-snapshot.*` 保留。最终采用上述唯一固定配方重新验收，未关闭 WebGL API 或注入 JS 修复。
- 五并发 pids 512 曾触顶、worker 5 秒 fixture 阈值曾超时、旧 wrapper 提前退出曾留下死 fixture Singleton；对应失败 gate 与 `pids512-failure-snapshot.tar.gz`、`stale-fixture-lock-failure-snapshot.tar.gz`、恢复记录保留。最终 pids 1024、worker bounded 30 秒、wait 所有子任务，并 graceful close 后取消 allocator；失败轮没有重标 PASS。
- 原 `ten` 10/10 测量的“无 --type”主进程候选峰值为 6。原采样没有保存单进程明细，原因无法回溯；带明细复跑的候选/独立根进程峰值均为 5，原计数 6 未复现，不能声称已证明是误分类。只采用最后一轮作为有界执行证据，原 gate 完整保留。
- 已运行的 1/5/10 gate 使用字节相同的 Go fixture 与 observation JS；最后十档案复跑仅增加 sampler 单进程明细及根进程分类。运行时 Go hash `a58bd1588e4800c9f37ef1cc219720a4b22a7373dd2f5dd969aa95bd20f91fd7`、JS hash `b5ddc06c4beb9e25b271b95a3ae5cc203b03a30545a09faf1354985e4c0b3cfe`；原 sampler hash `276dcf6515121f10747249e7822fc7f13dc08523ed0f3d659ddbce1936e1ad2e`、带进程明细的运行 sampler hash `8d9f465481a7952b522283b0f4078112a960d4f8c8e6cb963d6a57dcca43d825`。审查后仅修模式日志、共用 wait-all 区间 runner，并为 summary 增加 CPU 峰值派生字段；新源码只做语法检查、Go 编译与无浏览器聚合验收，没有新浏览器运行，不将旧 runtime 证据重标为新源码实验。
- 旧 storageOnly 日志错误地写成 fetch/opaque-redirect/guard passed，并声称记录 worker；实际跳过这些完整模式检查，仅完成初始化、导航、存储和轻量 Canvas。日志已改为按模式真实输出；旧日志文字错误在此记录，旧 raw 与旧语义断言保持原样。
- 早期批准 100 时曾生成 100 份纯 JSON 文本，随后用户将当前这轮测试调整为十个，删除其余 90 份；没有启动第 11 个 ShardX 档案或运行 100 个浏览器。本轮最终实际样本仅 fixture-001–010。
- 本地 fixtures 不能证明平台挑战可通过、真实账号登录可用或不同真实设备完全等价。母号/子号全部业务接线、互斥/排队、异常退出恢复、升级策略、真实子号登录与部署仍由 #10 验收；本轮没有更改生产业务。

## 复现与制品保留

基线 `6ed5e362c61a823cf118cf4c589d8316151aa4d0`；独立 worktree `/tmp/tsw-account-environment/worktree`。先将固定官方 ZIP 解压为 `/tmp/tsw-account-environment/shard-followup/engine/ShardX-Linux/`，复用上述依赖镜像，然后执行：

```sh
python3 tools/qualification/create-shard-recipes.py /tmp/tsw-account-environment/shard-followup/ShardX-Fingerprints.zip /tmp/tsw-account-environment/shard-followup/runtime-evidence/recipes
TSW_CHECK_WORKTREE=/tmp/tsw-account-environment/worktree PATH=/tmp/go/bin:$PATH python3 /tmp/tsw-account-environment/check.py reproduce-shard-compile /tmp/go/bin/go test -tags qualification -c ./internal/platform -o /tmp/tsw-account-environment/shard-followup/qualification.test
TSW_CHECK_WORKTREE=/tmp/tsw-account-environment/worktree python3 /tmp/tsw-account-environment/check.py reproduce-shard-single python3 tools/qualification/measure-shard.py reproduce-single 1
TSW_CHECK_WORKTREE=/tmp/tsw-account-environment/worktree python3 /tmp/tsw-account-environment/check.py reproduce-shard-five python3 tools/qualification/measure-shard.py reproduce-five 5
TSW_CHECK_WORKTREE=/tmp/tsw-account-environment/worktree python3 /tmp/tsw-account-environment/check.py reproduce-shard-ten python3 tools/qualification/measure-shard.py reproduce-ten 5 storage
```

`check.py` 与固定复现源码、报告、staged diff/hash、最终证据 manifest 保存在 `shard-qualification-snapshot.*`。原始 JSON、日志、失败终态快照保留于隔离证据目录；没有新建验证镜像，所有测试容器 `--rm`，无回滚容器/镜像。

部署构建完成后可清理本任务 `shard-followup/ShardX-Linux.zip`（约 198 MiB）及可重新编译的 `shard-followup/qualification.test`（约 15 MiB）；`shard-followup/engine/`（约 607 MiB）应在 #10 制品使用与部署验收完成后再清理。测量/失败 gate、源码 snapshot、官方源码与许可证据须保留；当前不清理生产数据或其他项目镜像。
