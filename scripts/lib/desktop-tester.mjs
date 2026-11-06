// @ts-check
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import path from 'node:path';
import fs from 'node:fs';

const execFileAsync = promisify(execFile);

/**
 * 封装与 agent-desktop CLI 的底层无障碍交互
 */
export class DesktopDriver {
  /**
   * @param {object} [options]
   * @param {string} [options.cliCmd='agent-desktop']
   * @param {boolean} [options.verbose=false]
   */
  constructor(options = {}) {
    this.cliCmd = options.cliCmd || 'agent-desktop';
    this.verbose = options.verbose || false;
    this._hasBinaryCache = null;
  }

  /**
   * 检查系统 PATH 中是否存在可直接调用的二进制
   * @param {string} binName
   */
  async checkBinary(binName) {
    if (this._hasBinaryCache !== null) return this._hasBinaryCache;
    try {
      await execFileAsync('which', [binName]);
      this._hasBinaryCache = true;
    } catch {
      this._hasBinaryCache = false;
    }
    return this._hasBinaryCache;
  }

  /**
   * 执行 agent-desktop 命令并返回解析后的 JSON
   * @param {string[]} args
   * @returns {Promise<{ ok: boolean, data?: any, error?: any }>}
   */
  async exec(args) {
    try {
      // 优先直接调用 agent-desktop，若未安装至 PATH 则退回使用 npx
      let cmd = this.cliCmd;
      let callArgs = args;
      if (cmd === 'agent-desktop') {
        const hasBinary = await this.checkBinary('agent-desktop');
        if (!hasBinary) {
          callArgs = ['agent-desktop', ...args];
          cmd = 'npx';
        }
      }

      if (this.verbose) {
        console.log(`[DesktopDriver] > ${cmd} ${callArgs.join(' ')}`);
      }

      const { stdout } = await execFileAsync(cmd, callArgs, {
        maxBuffer: 20 * 1024 * 1024,
        timeout: 30000,
        env: { ...process.env, PATH: process.env.PATH },
      });

      return JSON.parse(stdout.trim());
    } catch (err) {
      if (err.stdout) {
        try {
          return JSON.parse(err.stdout.trim());
        } catch {
          // ignore
        }
      }
      return {
        ok: false,
        error: {
          code: 'EXEC_ERROR',
          message: err.message || String(err),
        },
      };
    }
  }

  /**
   * 列出指定应用的窗口
   * @param {string} appName
   */
  async listWindows(appName) {
    const res = await this.exec(['list-windows', '--app', appName]);
    if (!res.ok) {
      throw new Error(`Failed to list windows for ${appName}: ${res.error?.message}`);
    }
    return res.data || [];
  }

  /**
   * 获取指定窗口的无障碍树快照
   * @param {string} windowId
   * @param {object} [opts]
   * @param {boolean} [opts.interactiveOnly=true]
   * @param {boolean} [opts.compact=true]
   */
  async snapshot(windowId, opts = { interactiveOnly: true, compact: true }) {
    const args = ['snapshot', '--window-id', windowId];
    if (opts.interactiveOnly) args.push('-i');
    if (opts.compact) args.push('--compact');
    args.push('--force-electron-a11y');

    const res = await this.exec(args);
    if (!res.ok) {
      throw new Error(`Snapshot failed for window ${windowId}: ${res.error?.message}`);
    }
    return res.data;
  }

  /**
   * 在树中递归查找符合条件的元素
   * @param {object} treeNode
   * @param {(node: any) => boolean} predicate
   * @returns {any | null}
   */
  findElement(treeNode, predicate) {
    if (!treeNode) return null;
    if (predicate(treeNode)) return treeNode;
    if (Array.isArray(treeNode.children)) {
      for (const child of treeNode.children) {
        const found = this.findElement(child, predicate);
        if (found) return found;
      }
    }
    return null;
  }

  /**
   * 在树中递归查找所有符合条件的元素
   * @param {object} treeNode
   * @param {(node: any) => boolean} predicate
   * @returns {any[]}
   */
  findAllElements(treeNode, predicate) {
    const results = [];
    const traverse = (node) => {
      if (!node) return;
      if (predicate(node)) results.push(node);
      if (Array.isArray(node.children)) {
        for (const child of node.children) traverse(child);
      }
    };
    traverse(treeNode);
    return results;
  }

  /**
   * 点击指定的 Ref
   * @param {string} refId
   */
  async click(refId) {
    const res = await this.exec(['click', refId]);
    if (!res.ok) {
      throw new Error(`Click failed on ${refId}: ${res.error?.message}`);
    }
    return res.data;
  }

  /**
   * 设置元素的值
   * @param {string} refId
   * @param {string} value
   */
  async setValue(refId, value) {
    const res = await this.exec(['set-value', refId, value]);
    if (!res.ok) {
      throw new Error(`SetValue failed on ${refId}: ${res.error?.message}`);
    }
    return res.data;
  }

  /**
   * 截取窗口快照保存为 PNG
   * @param {string} windowId
   * @param {string} outputPath
   */
  async screenshot(windowId, outputPath) {
    const dir = path.dirname(outputPath);
    if (!fs.existsSync(dir)) {
      fs.mkdirSync(dir, { recursive: true });
    }
    const res = await this.exec(['screenshot', '--window-id', windowId, outputPath]);
    if (!res.ok) {
      throw new Error(`Screenshot failed on ${windowId}: ${res.error?.message}`);
    }
    return res.data;
  }
}

/**
 * 冒烟测试套件组织器
 */
export class SmokeTestSuite {
  /**
   * @param {string} suiteName
   */
  constructor(suiteName) {
    this.suiteName = suiteName;
    this.tests = [];
    this.driver = new DesktopDriver();
  }

  /**
   * 注册一个测试用例
   * @param {string} name
   * @param {(driver: DesktopDriver, ctx: Record<string, any>) => Promise<void>} fn
   */
  test(name, fn) {
    this.tests.push({ name, fn });
  }

  /**
   * 运行所有测试用例并输出报告
   * @param {Record<string, any>} [initialContext={}]
   */
  async run(initialContext = {}) {
    console.log(`\n\x1b[1m\x1b[34m=== [Smoke Test Suite] ${this.suiteName} ===\x1b[0m\n`);
    const startTime = Date.now();
    let passed = 0;
    let failed = 0;
    const ctx = { ...initialContext };

    for (const { name, fn } of this.tests) {
      process.stdout.write(`  ⏳ ${name.padEnd(45, ' ')} `);
      const caseStart = Date.now();
      try {
        await fn(this.driver, ctx);
        const duration = Date.now() - caseStart;
        console.log(`\x1b[32m✔ PASS\x1b[0m (${duration}ms)`);
        passed++;
      } catch (err) {
        const duration = Date.now() - caseStart;
        console.log(`\x1b[31m✖ FAIL\x1b[0m (${duration}ms)`);
        console.error(`     \x1b[31m└─ Error: ${err.message}\x1b[0m`);
        failed++;

        // 尝试自动截屏留存证据
        if (ctx.activeWindowId) {
          const failScreenPath = path.resolve(
            process.cwd(),
            '.test-output',
            `smoke-fail-${Date.now()}.png`
          );
          try {
            await this.driver.screenshot(ctx.activeWindowId, failScreenPath);
            console.log(`     \x1b[33m└─ [Screenshot saved] ${failScreenPath}\x1b[0m`);
          } catch {
            // ignore
          }
        }
      }
    }

    const totalDuration = ((Date.now() - startTime) / 1000).toFixed(2);
    console.log(`\n\x1b[1m--------------------------------------------------\x1b[0m`);
    console.log(
      `\x1b[1mResult: \x1b[32m${passed} Passed\x1b[0m, \x1b[31m${failed} Failed\x1b[0m (${totalDuration}s total)`
    );
    console.log(`\x1b[1m--------------------------------------------------\x1b[0m\n`);

    return failed === 0;
  }
}
