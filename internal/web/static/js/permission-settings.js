import { api } from './api.js';

export class PermissionSettings {
  constructor() {
    this.form = document.getElementById('permission-defaults-form');
    this.general = document.getElementById('permission-default-mode');
    this.feishu = document.getElementById('permission-feishu-mode');
    this.status = document.getElementById('permission-defaults-status');
    this.refresh = document.getElementById('permission-defaults-refresh');
    this.pending = false; this.available = false;
    this.form.onsubmit = event => { event.preventDefault(); this.update({default_mode:this.general.value, feishu_mode:this.feishu.value}); };
    this.refresh.onclick = () => this.update();
  }
  async update(payload) {
    if (this.pending) return;
    this.pending = true;
    this.form.querySelectorAll('select,button').forEach(el => { el.disabled = true; });
    this.status.textContent = payload ? '正在保存…' : '正在读取…';
    this.status.classList.remove('is-error');
    try {
      const data = payload ? await api.post('/settings/permissions', payload) : await api.get('/settings/permissions');
      if (![data?.default_mode, data?.feishu_mode].every(mode => ['ask','full'].includes(mode))) throw Error('主机未返回有效的权限配置');
      this.general.value = data.default_mode; this.feishu.value = data.feishu_mode;
      this.available = true;
      this.status.textContent = payload ? '已保存，新会话生效；已有会话保留各自的权限选择。' : '';
    } catch (error) {
      this.available = false;
      this.status.textContent = error.status === 404 ? '请更新运行主机后配置默认权限。' : `操作失败：${error.message}`;
      this.status.classList.add('is-error');
    } finally {
      this.pending = false;
      this.form.querySelectorAll('select,button').forEach(el => { el.disabled = !this.available; });
      this.refresh.disabled = false;
    }
  }
}
