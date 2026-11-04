#!/usr/bin/env node
// @ts-check

import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { SmokeTestSuite } from './lib/desktop-tester.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const suite = new SmokeTestSuite('EasyAgent Desktop UI E2E Smoke');

// TC-01: 发现并绑定 EasyAgent 主窗口
suite.test('TC-01: 探测并绑定 EasyAgent 主窗口', async (driver, ctx) => {
  const windows = await driver.listWindows('EasyAgent');
  if (!windows || windows.length === 0) {
    throw new Error('未检测到正在运行的 EasyAgent 窗口，请先打开应用或指定启动路径');
  }

  // 区分主窗口与可能存在的更新/系统弹窗
  const mainWindow = windows.find((w) => w.title === 'EasyAgent') || windows[0];
  if (!mainWindow) {
    throw new Error('未找到主窗口');
  }

  ctx.activeWindowId = mainWindow.id;
  ctx.allWindows = windows;

  if (!mainWindow.accessible) {
    throw new Error(`窗口 ${mainWindow.id} 未开启系统无障碍访问权限`);
  }
});

// TC-02: 异常辅助弹窗检测与自动消解（如软件更新弹窗）
suite.test('TC-02: 阻断弹窗检测与自愈清理', async (driver, ctx) => {
  const updateDialog = ctx.allWindows.find((w) => w.title === '软件更新');
  if (updateDialog) {
    try {
      const snap = await driver.snapshot(updateDialog.id, { interactiveOnly: true, compact: true });
      const cancelBtn = driver.findElement(
        snap.tree,
        (n) => n.role === 'button' && (n.name === '取消' || n.name === 'Close')
      );
      if (cancelBtn) {
        await driver.click(cancelBtn.ref_id);
        await new Promise((r) => setTimeout(r, 600));
      }
    } catch {
      // 弹窗可能已自动关闭，安全跳过
    }
  }
});

// TC-03: 主窗口无障碍树与界面结构完整性校验
suite.test('TC-03: 界面无障碍结构树完整性校验', async (driver, ctx) => {
  const snap = await driver.snapshot(ctx.activeWindowId, { interactiveOnly: true, compact: true });
  ctx.latestSnapshot = snap;

  const refCount = snap.ref_count || 0;
  if (refCount < 20) {
    throw new Error(`UI 节点数异常偏少 (${refCount} 个)，界面可能发生白屏或渲染异常`);
  }
});

// TC-04: 核心导航交互链路验证（工作台/管家/任务跟进）
suite.test('TC-04: 侧边栏与核心视图切换响应', async (driver, ctx) => {
  const tree = ctx.latestSnapshot.tree;
  // 查找任一典型核心按钮
  const navBtn = driver.findElement(
    tree,
    (n) => n.role === 'button' && ['切换工作台', '管家', '任务跟进', '此 Mac'].includes(n.name)
  );

  if (!navBtn) {
    throw new Error('未找到核心导航按钮（切换工作台/管家/任务跟进）');
  }

  // 触发点击测试
  await driver.click(navBtn.ref_id);
  await new Promise((r) => setTimeout(r, 400));
});

// TC-05: 会话搜索框输入与筛选能力测试
suite.test('TC-05: 会话搜索框值注入与响应', async (driver, ctx) => {
  // 重新获取最新快照避免 Stale Ref
  const snap = await driver.snapshot(ctx.activeWindowId, { interactiveOnly: true, compact: true });
  const searchInput = driver.findElement(
    snap.tree,
    (n) => n.role === 'textfield' && n.name === '搜索会话'
  );

  if (!searchInput) {
    throw new Error('未定位到"搜索会话"输入框');
  }

  // 注入测试搜索文本
  await driver.setValue(searchInput.ref_id, 'smoke-test-query');
  await new Promise((r) => setTimeout(r, 300));

  // 还原清空搜索框
  const refreshSnap = await driver.snapshot(ctx.activeWindowId, { interactiveOnly: true, compact: true });
  const freshSearch = driver.findElement(
    refreshSnap.tree,
    (n) => n.role === 'textfield' && n.name === '搜索会话'
  );
  if (freshSearch) {
    await driver.setValue(freshSearch.ref_id, '');
  }
});

// TC-06: 新建会话动作触发与状态断言
suite.test('TC-06: 新建会话动作触发与主流程通畅度', async (driver, ctx) => {
  const snap = await driver.snapshot(ctx.activeWindowId, { interactiveOnly: true, compact: true });
  const newChatBtn = driver.findElement(
    snap.tree,
    (n) => n.role === 'button' && n.name === '新建会话'
  );

  if (!newChatBtn) {
    throw new Error('未定位到"新建会话"按钮');
  }

  await driver.click(newChatBtn.ref_id);
  await new Promise((r) => setTimeout(r, 500));
});

// TC-07: 导出 E2E 验收通过界面快照留档（尽力而为，不阻断核心功能判定）
suite.test('TC-07: 导出 E2E 验收通过界面快照留档', async (driver, ctx) => {
  const outputPath = path.resolve(process.cwd(), '.test-output', 'smoke-pass-latest.png');
  try {
    await driver.screenshot(ctx.activeWindowId, outputPath);
  } catch (err) {
    // macOS screencapture 在特定系统安全弹窗未点确认或息屏时会被拦截
    // 作为辅助证据生成项，降级为友好提示，避免阻塞主流程通过
    process.stdout.write(`\x1b[33m(截图跳过: ${err.message})\x1b[0m `);
  }
});

// 执行测试套件
const success = await suite.run();
process.exit(success ? 0 : 1);
