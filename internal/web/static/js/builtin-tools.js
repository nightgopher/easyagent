import { api } from './api.js';

const ids = ['file_read', 'file_write', 'bash', 'web_search', 'web_fetch'];
const sources = {startup:'启动配置', yaml:'YAML 配置', environment:'环境变量', saved:'主机设置', command:'启动参数'};
const restrictions = {allowed_tools:'允许工具名单', blocked_tools:'禁用工具名单'};

export class BuiltinToolSettings {
  constructor() {
    this.controls = Object.fromEntries(ids.map(id => [id, {
      input: document.getElementById(`builtin-${id}`),
      status: document.getElementById(`builtin-${id}-status`),
    }]));
    this.status = document.getElementById('builtin-tools-status');
    this.refresh = document.getElementById('builtin-tools-refresh');
    this.pending = false; this.current = null;
    for (const id of ids) this.controls[id].input.onchange = () => this.update({tool:id, enabled:this.controls[id].input.checked});
    this.refresh.onclick = () => this.update();
  }
  validate(data) {
    if (!Array.isArray(data?.tools)) return null;
    const settings = new Map();
    for (const item of data.tools) {
      if (!item || typeof item.id !== 'string' || settings.has(item.id) || typeof item.enabled !== 'boolean' || typeof item.editable !== 'boolean' ||
          !['enabled','partial','disabled'].includes(item.effective) || !Object.hasOwn(sources, item.source)) return null;
      settings.set(item.id, item);
    }
    return ids.every(id => settings.has(id)) ? settings : null;
  }
  render() {
    for (const id of ids) {
      const {input, status} = this.controls[id];
      const item = this.current?.get(id);
      input.disabled = this.pending || !item?.editable;
      if (!item) { status.textContent = ''; continue; }
      input.checked = item.enabled;
      let effect = item.enabled ? '已启用' : '已关闭';
      if (item.enabled && item.effective === 'partial') effect = '部分可用：受工具过滤配置限制';
      if (item.enabled && item.effective === 'disabled') effect = '不可用：受工具过滤配置限制';
      const sourceDetail = typeof item.source_detail === 'string' && item.source_detail ? ` (${item.source_detail})` : '';
      const reasons = Array.isArray(item.restricted_by) ? item.restricted_by.map(key => restrictions[key]).filter(Boolean) : [];
      status.textContent = `来源：${sources[item.source]}${sourceDetail} · ${effect}${reasons.length ? ` (${reasons.join(', ')})` : ''}`;
    }
    this.refresh.disabled = this.pending;
  }
  async update(payload) {
    if (this.pending) return;
    this.pending = true; this.render();
    this.status.textContent = payload ? '正在保存…' : '正在读取…';
    this.status.classList.remove('is-error');
    try {
      const data = payload ? await api.post('/settings/builtin-tools', payload) : await api.get('/settings/builtin-tools');
      const settings = this.validate(data);
      if (!settings) throw Error('主机未返回有效的工具设置');
      this.current = settings;
      this.status.textContent = payload ? '已保存，新会话生效。' : '';
    } catch (error) {
      this.status.textContent = error.status === 404 ? '请更新运行主机后配置内置工具。' : `操作失败：${error.message}`;
      this.status.classList.add('is-error');
    } finally {
      this.pending = false; this.render();
    }
  }
}
