# 听歌习惯沉淀与推荐闭环：现状与缺口

状态：提案（仅分析，不含实现）。日期：2026-11-02。

## 背景与动机

场景目标：让音乐助手随使用时间「越推越准」——沉淀用户的听歌习惯（长期口味演化），推荐有依据，并从用户对推荐的实际反应（播放/跳过/红心）中修正后续推荐，形成闭环。

外部参照（2026-11 调研）：网易云音乐官方推出开放平台 CLI `ncm-cli`（npm `@music163/ncm-cli`）与官方 Agent Skills 包 [NetEase/skills](https://github.com/NetEase/skills)（Apache-2.0）。其智能助手技能用状态文件沉淀偏好（`~/.config/ncm/ncm-preference.json`：整体画像、近 20 首趋势、红心时段规律、内容标签，24h 缓存；`ncm-history.json`：已推荐去重），数据源是红心歌单（最多 200 首，含红心时间戳与内容标签）。它的局限：每次从滚动窗口重算、只认红心一种信号、没有「推荐 → 用户反应 → 修正」的闭环。

对照发现：easyagent 已有成型的 music 子系统（见下节），底子比 ncm-cli 技能包更完整——多音源、持久播放历史、跨 agent 统一画像。但在信号丰富度、个性化推荐、反馈归因、时间演化、主动触发五个方向上有明确缺口。本文记录现状与缺口供排期参考，**本 PR 只含此文档，无代码改动**。

## 现状盘点（已具备的能力）

| 能力 | 位置 | 说明 |
|---|---|---|
| music Application | `internal/agents/music/` | 独立工具集/系统提示/会话扩展状态；7 个具名工具 `music_search/play/lyrics/detail/playlist/recommend/history`（`tools/tools.go:20`） |
| 双音源 | `internal/music/` | netease + bilibili 适配器，统一 `SourceRouter` |
| 播放历史 | `internal/music/pref/store.go:25` | 环形缓冲 500 条播放记录 + 聚合（艺人/音源/单曲计数），注入固定约 40 token |
| 统一画像 | `internal/profile/store.go` | 跨 agent「第二大脑」：category→key→Fact，last-write-wins，音乐类上限 20 条（`store.go:47`），固定约 80 token 摘要注入所有 agent |
| 会话事实抽取 | `internal/profile/extractor.go` | 会话结束时 LLM 扫描最近消息抽取用户事实，非阻塞 |
| 标准技能加载 | `sdk/skill/skill.go:73` | 递归查找 SKILL.md + frontmatter，兼容 NetEase/skills 的目录结构 |
| `/loop` 会话循环 | `internal/scheduler/loop.go` | 会话内定期注入 prompt（goroutine + Ticker） |

架构判断依据 [Skills 与 Application 的边界](decisions/skills-vs-application.md)：music 已是 Application（有独立运行状态），因此演进方向是**在该 Application 内补具名工具**，而不是引入 ncm-cli 技能走通用 bash（理由见缺口五）。

## 缺口分析

### 缺口一：反馈信号只有「播放成功」，无正负反馈（最核心）

`pref.PlayRecord`（`internal/music/pref/store.go:30`）只在播放成功后记录 song/artist/source/played_at。缺：

- **负信号**：跳过、播放未完成、显式「不喜欢」。「更好听」的一半输入在这一侧，现在完全没有；
- **显式正信号**：红心/收藏。当前网易云适配器是匿名 Web API（`internal/music/netease/client.go`，随机 UA、无登录态），拿不到红心与个人歌单，也不能创建/写入歌单；
- 播放时长/完播等强度信息（重复播放经 `TopSongs` 计数有一定体现，但无法区分「听完」与「播了就切」）。

对照 ncm-cli：其画像以红心为最强显式信号（开放平台 appId + privateKey 鉴权）。不接登录态，习惯沉淀只能靠隐式播放行为，天花板明显。

### 缺口二：推荐与画像断链

`music_recommend`（`internal/agents/music/tools/recommend.go`）只提供公共新歌/排行榜/热门，不消费 `pref`/`profile`。个性化完全依赖系统提示里的固定摘要 + 模型自行组合 `music_search` 关键词。缺「基于画像的个性化检索」路径：相似艺人/曲风扩展、按画像对排行与新歌重排。

### 缺口三：推荐无归因，闭环断裂

没有「推荐事件」概念：`PlayRecord` 不区分自然播放与「来自某次推荐的播放」，也不记录推荐批次与理由。于是无法回答「上次推的 5 首后来听了几首、跳了几首」，推荐策略无法按结果修正。这是 ncm-cli 同样缺失、而 easyagent 的结构化工具 + 持久存储恰好适合补上的部分。

### 缺口四：画像无时间维度

- `profile.Store` 按 key last-write-wins，覆盖即丢历史，做不了口味演化（如「电子乐占比 15%→40%」）；
- `pref` 聚合是全量统计，无 7d/30d 趋势窗口；
- `SessionExtractor` 每会话仅运行一次、只扫最近消息，抽的是「当前事实」而非「变化轨迹」。

### 缺口五：主动触发缺宿主

「每天早上 9 点推一拨歌」需要持久定时器。现状：`/loop` 是会话级内存态（`internal/scheduler/loop.go:59`，进程重启即丢，且必须绑定已存在会话）；Workflow 已于 2026-10-17 整体移除（[工作流设计](decisions/workflow-engine.md)）。短期可用系统 launchd/cron 定时起 CLI 单次任务替代，零代码；长期可评估 `/loop` 持久化。

关联约束：若改走 ncm-cli 技能路线，其全部命令经 bash 执行，而 `EnableBash` 默认 false（`sdk/config/config.go:152`），且 serve/飞书入口未注入 ConfirmFunc 时确认门默认放行（`sdk/agent/loop.go:678`）。走 Application 具名工具路线天然绕开这两个问题——这也是本文方向选择的依据之一。

## 方向建议（按优先级，本文不实现）

| 优先级 | 方向 | 要点 | 量级 |
|---|---|---|---|
| P1 | 网易云登录态接入 | 二选一：开放平台 API（ncm-cli 同款 appId+privateKey，合规，能拿红心/个人歌单/写歌单）或 Web cookie（逆向性质、易失效，不推荐）。在 `internal/music/netease` 加鉴权，新增 `music_like`、个人曲库类具名工具 | 中 |
| P2 | 信号模型扩展 | `PlayRecord` 增加播放时长/完成度；新增跳过与「踩」信号；新增 `RecommendationEvent`（批次、候选、推荐理由、归因），与后续播放/跳过记录 join 成闭环 | 小-中 |
| P3 | 画像时间维度 | `pref` 增加 7d/30d 趋势聚合；`profile` 事实保留更新历史（或快照序列），支持「近一个月口味变化」表述 | 小 |
| P4 | 个性化推荐路径 | `music_recommend` 消费画像：相似艺人/标签扩展检索、按画像对排行/新歌重排；推荐理由必须引用画像证据（对齐 ncm-cli 助手的两层说明） | 中 |
| P5 | 定时主动推荐 | 短期：文档化 launchd + CLI 单次任务；长期：评估 `/loop` 持久化与无会话触发 | 小/中 |

排序理由：没有 P1/P2 的信号与归因，P3/P4 是无米之炊；P5 独立于主线，可并行。

## 与既有决策的关系

- [Skills 与 Application 的边界](decisions/skills-vs-application.md)：music 已是 Application，本文所有方向都在该 Application 内演进，不新增入口。ncm-cli 技能包可作为产品形态参照，不建议以 bash 技能形式引入。
- [工作流设计](decisions/workflow-engine.md)：Workflow 已下线，本文不依赖任何 Workflow 能力。
- 通用记忆工具 `save_memory`（`sdk/tools/memory.go:116`，追加式扁平文本、整文件注入上下文）适合存「长期事实」，不适合承载事件序列；本文的偏好数据走 `pref`/`profile` 结构化存储，不改动 `save_memory`。

---

依据：[ncm-cli 官方页](https://music.163.com/st/ncmcli)、[NetEase/skills](https://github.com/NetEase/skills)；本文源码行号以 2026-11-02 main（`477d65c3`）为准。
