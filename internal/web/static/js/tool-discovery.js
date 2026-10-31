import { api } from './api.js';

export class ToolDiscoverySettings {
  constructor() {
    this.mode = document.getElementById('tool-discovery-mode');
    this.saveButton = document.getElementById('tool-discovery-save');
    this.refresh = document.getElementById('tool-discovery-refresh');
    this.status = document.getElementById('tool-discovery-status');
    this.pending = false; this.available = false;
    document.getElementById('tool-discovery-form').onsubmit = event => { event.preventDefault(); this.update(this.mode.value); };
    this.refresh.onclick = () => this.update();
  }
  async update(mode) {
    if (this.pending) return;
    this.pending = true;
    this.mode.disabled = this.saveButton.disabled = this.refresh.disabled = true;
    this.status.textContent = mode ? '正在保存…' : '正在读取…';
    this.status.classList.remove('is-error');
    try {
      const data = mode ? await api.post('/settings/tool-discovery', {mode}) : await api.get('/settings/tool-discovery');
      if (!['off', 'local'].includes(data?.mode)) throw Error('运行主机不支持此设置，请更新后重试。');
      this.mode.value = data.mode; this.available = true;
      this.status.textContent = mode ? '已保存，下一次任务生效。' : '';
    } catch (error) {
      this.available = false;
      this.status.textContent = error.status === 404 ? '需要更新运行主机后使用工具按需加载。' : `操作失败：${error.message}`;
      this.status.classList.add('is-error');
    } finally {
      this.pending = false; this.refresh.disabled = false;
      this.mode.disabled = this.saveButton.disabled = !this.available;
    }
  }
}
